package static

import (
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestDir(t *testing.T) {
	victim, err := ioutil.TempDir("", "statictest-")
	if err != nil {
		t.Fatalf("tempdir: %s", err)
	}
	defer os.RemoveAll(victim)

	t.Logf("Using temporary directory: %q", victim)

	dir := Dir(victim)
	defer dir.Close()

	testFile := filepath.Join(victim, "test")
	ioutil.WriteFile(testFile, nil, 0644)
	ioutil.WriteFile(testFile, []byte("foo"), 0644)
	os.Remove(testFile)

	time.Sleep(10 * time.Millisecond)
}
