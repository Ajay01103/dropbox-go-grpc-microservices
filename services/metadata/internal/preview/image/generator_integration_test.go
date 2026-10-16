//go:build integration

// End-to-end verification of the image family against the real dev
// infrastructure (RustFS), mirroring how the worker runs:
//
//	go test -tags integration ./internal/preview/...
//
// Blocks are seeded as real S3 objects, so this exercises the whole path the
// worker takes: stream the blocks to a scratch file, decode from that file,
// store the preview. Two properties only a real reassembly can show are pinned
// here — the block ORDER decides whether a preview exists at all, and a run
// leaves no scratch directory behind.
package image

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"os"
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

// previewTestBucket and credentials match the dev compose stack (RustFS),
// exactly as the metadata service is configured to reach it.
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
// (common.blockObjectKey). Spelled out here so the bytes are seeded where the
// uploader would have put them, not merely where the worker happens to look.
func previewTestBlockKey(hash string) string {
	return fmt.Sprintf("blocks/%s/%s/%s", hash[:2], hash[2:4], hash)
}

// previewTestPutBlock stores one block and returns its hex hash. RustFS being
// down skips the test rather than failing it.
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

func TestIntegrationImagePreviewFromRealBlocks(t *testing.T) {
	storage := previewTestStorage(t)
	client := previewTestS3Client(t)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 800x400: wider than the 512px cap, so the stored preview's dimensions
	// prove the decoder saw the whole source.
	source := image.NewRGBA(image.Rect(0, 0, 800, 400))
	for y := 0; y < 400; y++ {
		for x := 0; x < 800; x++ {
			if x < 400 {
				source.Set(x, y, color.RGBA{R: 200, G: 40, B: 40, A: 255})
			} else {
				source.Set(x, y, color.RGBA{R: 40, G: 40, B: 200, A: 255})
			}
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, source, &jpeg.Options{Quality: 92}); err != nil {
		t.Fatalf("encode fixture: %v", err)
	}
	data := encoded.Bytes()

	// Split inside the compressed scan data: neither half is a decodable image,
	// so only an in-order reassembly can produce a preview.
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

	const fileID = "it-preview-image-file"
	key := common.PerFileThumbnailKey(fileID, hashes)
	t.Cleanup(func() {
		_ = storage.DeleteThumbnail(context.Background(), fileID, key)
		_ = storage.DeleteThumbnail(context.Background(), fileID+"-reversed", common.PerFileThumbnailKey(fileID+"-reversed", hashes))
	})

	generator := &Generator{storage: storage, logger: zap.NewNop()}
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

	// Read the stored preview back and check the dimensions the source implies.
	stored, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(previewTestBucket), Key: aws.String(key)})
	if err != nil {
		t.Fatalf("read stored preview: %v", err)
	}
	preview, err := jpeg.Decode(stored.Body)
	stored.Body.Close()
	if err != nil {
		t.Fatalf("decode stored preview: %v", err)
	}
	if preview.Bounds().Dx() != 512 || preview.Bounds().Dy() != 256 {
		t.Fatalf("preview is %v, want 512x256 (source 800x400 scaled to the 512 cap)", preview.Bounds())
	}

	// Same blocks, reversed: the reassembly must fail, and permanently — no
	// retry turns a mis-ordered concatenation into an image.
	reversedKey := common.PerFileThumbnailKey(fileID+"-reversed", hashes)
	_, err = generator.GenerateToKey(ctx, reversedKey, []string{second, first})
	if err == nil {
		t.Fatal("reversed block order produced a preview")
	}
	if !common.IsPermanent(err) {
		t.Fatalf("reversed-order failure %v must be permanent", err)
	}
	if exists, err := storage.ThumbnailExists(ctx, reversedKey); err != nil || exists {
		t.Fatalf("reversed-order run stored a preview (exists=%v, err=%v)", exists, err)
	}

	// A block that is not in storage stays transient: the object may simply not
	// be visible yet.
	_, err = generator.GenerateToKey(ctx, common.PerFileThumbnailKey(fileID+"-missing", hashes),
		[]string{first, strings.Repeat("34", 32)})
	if err == nil {
		t.Fatal("a missing block produced a preview")
	}
	if common.IsPermanent(err) {
		t.Fatalf("missing-block failure %v must stay transient", err)
	}

	previewTestAssertNoScratchDirs(t, scratchRoot)
}
