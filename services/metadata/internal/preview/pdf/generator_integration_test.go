//go:build integration

// End-to-end verification of the pdf family against the real dev
// infrastructure (RustFS) plus the real pdftoppm (Poppler) binary:
//
//	go test -tags integration ./internal/preview/...
//
// The fixture is a single-page PDF built in this file, so the case runs
// anywhere Poppler is installed rather than depending on a checked-in binary.
// What it drives is the same streamed path as the other families: real block
// objects in storage, a scratch file the sandboxed tool reads from, and a
// scratch directory that has to be gone afterwards.
package pdf

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	_ "image/jpeg"
	"math"
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

const (
	previewTestBucket   = "uploads"
	previewTestEndpoint = "http://localhost:9000"
	// Letter, in points: the aspect ratio the stored preview has to preserve.
	previewTestPageWidth  = 612
	previewTestPageHeight = 792
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

// previewTestPDF builds a minimal one-page PDF: a Letter-sized page with a blue
// rectangle filled across its lower-left area. The rectangle is what lets the
// test tell a real rasterization from a blank page, and the header/trailer
// structure is what makes a reversed reassembly unreadable rather than merely
// wrong.
func previewTestPDF() []byte {
	content := "0 0 1 rg 50 50 200 100 re f\n"
	objects := []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] /Contents 4 0 R /Resources << >> >>",
			previewTestPageWidth, previewTestPageHeight),
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
	}

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	offsets := make([]int, len(objects)+1)
	for i, body := range objects {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}
	// Every xref entry is exactly 20 bytes, which is what makes the offsets
	// above sufficient for a strict reader.
	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objects)+1)
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objects); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n", len(objects)+1, xref)
	return buf.Bytes()
}

func TestIntegrationPDFPreviewFromRealBlocks(t *testing.T) {
	storage := previewTestStorage(t)
	client := previewTestS3Client(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	generator, err := newPDFGenerator(storage, zap.NewNop())
	if err != nil {
		t.Skipf("pdf preview worker unavailable: %v", err)
	}

	// Split the document inside its objects: the first block holds the PDF
	// header and the catalog, so the reversed order is not a readable document.
	data := previewTestPDF()
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

	const fileID = "it-preview-pdf-file"
	const reversedID = "it-preview-pdf-file-reversed"
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

	stored, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(previewTestBucket), Key: aws.String(key)})
	if err != nil {
		t.Fatalf("read stored preview: %v", err)
	}
	preview, _, err := image.Decode(stored.Body)
	stored.Body.Close()
	if err != nil {
		t.Fatalf("decode stored preview: %v", err)
	}

	// pdftoppm rasters the page's longer edge (792pt) at 2x the final cap, so
	// the preview's longer edge is the 512 cap and its aspect ratio is the
	// page's — the source reached the tool intact.
	bounds := preview.Bounds()
	longEdge := max(bounds.Dx(), bounds.Dy())
	if longEdge != 512 {
		t.Fatalf("preview's longer edge is %d, want the 512 cap (bounds %v)", longEdge, bounds)
	}
	wantRatio := float64(previewTestPageWidth) / float64(previewTestPageHeight)
	gotRatio := float64(bounds.Dx()) / float64(bounds.Dy())
	if math.Abs(gotRatio-wantRatio) > 0.01 {
		t.Fatalf("preview aspect ratio %.4f does not match the page's %.4f (bounds %v)", gotRatio, wantRatio, bounds)
	}

	// And the page really was rendered: the blue rectangle has to be there.
	blue := 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r, g, b, _ := preview.At(x, y).RGBA()
			if b>>8 > 180 && r>>8 < 80 && g>>8 < 80 {
				blue++
			}
		}
	}
	if blue < 1000 {
		t.Fatalf("only %d blue pixels in the preview: the page rendered blank or the fixture is not being rasterized", blue)
	}

	// Reversed blocks: Poppler cannot read the document at all, which is
	// permanent — no retry assembles a header that is not there.
	if _, err := generator.GenerateToKey(ctx, reversedKey, []string{second, first}); err == nil {
		t.Fatal("reversed block order produced a preview")
	} else if !common.IsPermanent(err) {
		t.Fatalf("reversed-order failure %v must be permanent", err)
	}
	if exists, err := storage.ThumbnailExists(ctx, reversedKey); err != nil || exists {
		t.Fatalf("reversed-order run stored a preview (exists=%v, err=%v)", exists, err)
	}

	// A block that is not in storage stays transient.
	if _, err := generator.GenerateToKey(ctx, common.PerFileThumbnailKey(fileID+"-missing", hashes),
		[]string{first, strings.Repeat("34", 32)}); err == nil {
		t.Fatal("a missing block produced a preview")
	} else if common.IsPermanent(err) {
		t.Fatalf("missing-block failure %v must stay transient", err)
	}

	// The reassembly's scratch file, the page pdftoppm wrote, and the directory
	// holding both must all be gone.
	previewTestAssertNoScratchDirs(t, scratchRoot)
}
