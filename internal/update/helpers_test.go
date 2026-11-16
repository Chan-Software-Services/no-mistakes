package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"testing"

	"github.com/kunchenguid/no-mistakes/internal/testhome"
)

// testParentEnv marks the environment of a process this package's TestMain
// has set up, so a copy of the test binary that a test re-executes knows it
// is a child.
const testParentEnv = "NO_MISTAKES_UPDATE_TEST_PARENT"

func TestMain(m *testing.M) {
	if os.Getenv(testParentEnv) == "1" {
		os.Exit(runTestChild(m))
	}
	dirs, err := testhome.Setup("nm-update", "")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = os.Setenv("NO_MISTAKES_TELEMETRY", "off")
	_ = os.Setenv(testParentEnv, "1")

	os.Exit(dirs.Close(m.Run()))
}

// runTestChild runs a copy of this test binary that a test re-executed. Such
// a child never reaches a TestMain cleanup (a test kills it, or it exits in
// flag parsing on a flag only the CLI knows), so anything it made would stay
// in the temp dir: it makes nothing and uses the NM_HOME and HOME its
// parent's TestMain set.
func runTestChild(m *testing.M) int {
	for _, name := range []string{"NM_HOME", "HOME"} {
		if os.Getenv(name) == "" {
			fmt.Fprintf(os.Stderr, "test child started without its parent's %s\n", name)
			return 1
		}
	}
	return m.Run()
}

func stringsRepeat(s string, count int) string {
	buf := bytes.NewBuffer(nil)
	for i := 0; i < count; i++ {
		buf.WriteString(s)
	}
	return buf.String()
}

func makeTarGz(t *testing.T, files map[string][]byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range files {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(data))}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeZip(t *testing.T, files map[string][]byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
