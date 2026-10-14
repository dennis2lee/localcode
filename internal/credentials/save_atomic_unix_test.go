//go:build !windows

package credentials

import (
	"sync"
	"testing"
)

// A reader never sees half a credentials file. A running daemon reads it
// on each request that needs the key, while a login in another terminal
// may be writing it; a truncate-then-write save made one read in six of
// these fail to parse. (Found by review.) Unix only: Windows refuses to
// rename onto a file another process has open, which is a different
// failure and not a torn read.
func TestAReaderNeverSeesHalfASavedFile(t *testing.T) {
	home := t.TempDir()
	if err := SaveAnthropicAPIKey(home, "sk-ant-0"); err != nil {
		t.Fatal(err)
	}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				if err := SaveAnthropicAPIKey(home, "sk-ant-0"); err != nil {
					t.Error(err)
					return
				}
			}
		}
	}()
	bad := 0
	for i := 0; i < 5000; i++ {
		if c, err := Load(home); err != nil || c.AnthropicAPIKey != "sk-ant-0" {
			bad++
		}
	}
	close(stop)
	wg.Wait()
	if bad > 0 {
		t.Errorf("%d of 5000 reads saw a partial file", bad)
	}
}
