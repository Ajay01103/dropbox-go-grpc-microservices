//go:build integration

// End-to-end verification of the video family against the real dev
// infrastructure (RustFS) plus the real ffmpeg/ffprobe binaries:
//
//	go test -tags integration ./internal/preview/...
//
// This is the family the streamed reassembly exists for. The worker used to
// concatenate every block into a bytes.Buffer — up to 500 MB held in the heap,
// per job — and only then write it out for ffmpeg. It now writes each block
// straight to a scratch file, so the test drives the path that matters: real
// block objects in storage, a real container the sandboxed tools must read from
// a seekable path, and a scratch directory that has to be gone afterwards.
package video

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"go.uber.org/zap"

	"github.com/Ajay01103/go-dropbox/metadata/internal/preview/common"
)

const (
	previewTestBucket   = "uploads"
	previewTestEndpoint = "http://localhost:9000"
)

func previewTestStorage(t *testing.T) *common.Storage {
	t.Helper()
	storage, err := common.NewStorage(common.S3Config{
		Bucket:    previewTestBucket,
		Region:    "us-east-1",
		Endpoint:  previewTestEndpoint,
		AccessKey: "rustfsadmin",
		SecretKey: "rustfsadmin",
	})
	if err != nil {
		t.Skipf("preview storage unavailable: %v", err)
	}
	return storage
}

func previewTestS3Client(t *testing.T) *s3.Client {
	t.Helper()
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion("us-east-1"),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("rustfsadmin", "rustfsadmin", "")),
		awsconfig.WithEndpointResolverWithOptions(
			aws.EndpointResolverWithOptionsFunc(func(service, region string, options ...interface{}) (aws.Endpoint, error) {
				return aws.Endpoint{URL: previewTestEndpoint, SigningRegion: "us-east-1", HostnameImmutable: true}, nil
			}),
		),
	)
	if err != nil {
		t.Skipf("load S3 config: %v", err)
	}
	return s3.NewFromConfig(awsCfg, func(o *s3.Options) { o.UsePathStyle = true })
}

// previewTestBlockKey mirrors the shared block key scheme
// (common.blockObjectKey), so the fixture lands where the uploader would have
// written it.
func previewTestBlockKey(hash string) string {
	return fmt.Sprintf("blocks/%s/%s/%s", hash[:2], hash[2:4], hash)
}

func previewTestPutBlock(t *testing.T, ctx context.Context, client *s3.Client, data []byte) string {
	t.Helper()
	sum := sha256.Sum256(data)
	hash := hex.EncodeToString(sum[:])
	if _, err := client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(previewTestBucket),
		Key:           aws.String(previewTestBlockKey(hash)),
		Body:          bytes.NewReader(data),
		ContentLength: aws.Int64(int64(len(data))),
	}); err != nil {
		t.Skipf("RustFS unavailable (put block): %v", err)
	}
	return hash
}

// previewTestScratchRoot points the OS temp directory at a directory of this
// test's own for the rest of the test, and returns it. Reassembly scratch dirs
// then land inside it, so the "nothing was left behind" check can only see this
// test's runs — sibling packages' tests run in parallel and their in-flight
// scratch dirs would otherwise look like a leak.
//
// TMPDIR is the Unix knob, TMP and TEMP the Windows ones. A platform that
// ignores them cannot support the assertion, so the test is skipped rather than
// quietly weakened.
func previewTestScratchRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("TMPDIR", root)
	t.Setenv("TMP", root)
	t.Setenv("TEMP", root)
	if !strings.HasPrefix(os.TempDir(), root) {
		t.Skipf("this platform ignores TMPDIR/TMP/TEMP, so scratch dirs cannot be isolated to the test")
	}
	return root
}

// previewTestAssertNoScratchDirs fails when a run left a scratch directory in
// the test's own temp root.
func previewTestAssertNoScratchDirs(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read scratch root: %v", err)
	}
	for _, entry := range entries {
		if entry.IsDir() && strings.HasPrefix(entry.Name(), "preview-source-") {
			t.Fatalf("scratch dir %s was left behind", entry.Name())
		}
	}
}

// previewTestVideo builds a short MP4 with ffmpeg itself, so the fixture is a
// real container rather than a hand-assembled one.
func previewTestVideo(t *testing.T, ffmpeg string) []byte {
	t.Helper()
	out := filepath.Join(t.TempDir(), "fixture.mp4")
	cmd := exec.Command(ffmpeg,
		"-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=1:size=320x240:rate=10",
		"-pix_fmt", "yuv420p",
		"-y", out,
	)
	if err := cmd.Run(); err != nil {
		t.Skipf("ffmpeg cannot build the test fixture: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("ffmpeg produced an empty fixture")
	}
	return data
}

func TestIntegrationVideoPreviewFromRealBlocks(t *testing.T) {
	storage := previewTestStorage(t)
	client := previewTestS3Client(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	generator, err := newVideoGenerator(storage, zap.NewNop())
	if err != nil {
		t.Skipf("video preview worker unavailable: %v", err)
	}

	// Split the container inside its data: the first block holds the moov/mdat
	// header, so a reversed reassembly is not a playable file.
	data := previewTestVideo(t, generator.ffmpeg)
	split := len(data) / 3
	first := previewTestPutBlock(t, ctx, client, data[:split])
	second := previewTestPutBlock(t, ctx, client, data[split:])
	hashes := []string{first, second}
	t.Cleanup(func() {
		for _, hash := range hashes {
			_, _ = client.DeleteObject(context.Background(), &s3.DeleteObjectInput{
				Bucket: aws.String(previewTestBucket), Key: aws.String(previewTestBlockKey(hash)),
			})
		}
	})

	const fileID = "it-preview-video-file"
	const reversedID = "it-preview-video-file-reversed"
	key := common.PerFileThumbnailKey(fileID, hashes)
	reversedKey := common.PerFileThumbnailKey(reversedID, hashes)
	t.Cleanup(func() {
		_ = storage.DeleteThumbnail(context.Background(), fileID, key)
		_ = storage.DeleteThumbnail(context.Background(), reversedID, reversedKey)
	})

	scratchRoot := previewTestScratchRoot(t)

	got, err := generator.GenerateToKey(ctx, key, hashes)
	if err != nil {
		t.Fatalf("GenerateToKey: %v", err)
	}
	if got != key {
		t.Fatalf("GenerateToKey returned %q, want %q", got, key)
	}
	if exists, err := storage.ThumbnailExists(ctx, key); err != nil || !exists {
		t.Fatalf("preview object missing after generation (exists=%v, err=%v)", exists, err)
	}

	// The extracted frame is a JPEG of the 320x240 fixture: within the 512 cap,
	// so its dimensions come straight from the video the reassembly produced.
	stored, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(previewTestBucket), Key: aws.String(key)})
	if err != nil {
		t.Fatalf("read stored preview: %v", err)
	}
	frame, _, err := image.Decode(stored.Body)
	stored.Body.Close()
	if err != nil {
		t.Fatalf("decode stored preview: %v", err)
	}
	if frame.Bounds().Dx() != 320 || frame.Bounds().Dy() != 240 {
		t.Fatalf("preview is %v, want 320x240 (the fixture's frame size)", frame.Bounds())
	}

	// Reversed blocks: ffprobe cannot read the container, which is permanent.
	if _, err := generator.GenerateToKey(ctx, reversedKey, []string{second, first}); err == nil {
		t.Fatal("reversed block order produced a preview")
	} else if !common.IsPermanent(err) {
		t.Fatalf("reversed-order failure %v must be permanent", err)
	}
	if exists, err := storage.ThumbnailExists(ctx, reversedKey); err != nil || exists {
		t.Fatalf("reversed-order run stored a preview (exists=%v, err=%v)", exists, err)
	}

	// The reassembly's scratch file, the frame ffmpeg wrote, and the directory
	// holding both must all be gone.
	previewTestAssertNoScratchDirs(t, scratchRoot)
}
