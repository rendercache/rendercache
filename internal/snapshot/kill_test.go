// SPDX-License-Identifier: Apache-2.0
package snapshot

import (
	"bufio"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/rendercache/rendercache/internal/render"
)

const killHelperEnv = "RENDERCACHE_KILL_HELPER_DIR"

// TestKilledWriterHelper is not a test on its own: TestKilledWriterNeverCorruptsSnapshot
// starts it as a child process that saves one page over and over until killed.
func TestKilledWriterHelper(t *testing.T) {
	dir := os.Getenv(killHelperEnv)
	if dir == "" {
		t.Skip("helper process for TestKilledWriterNeverCorruptsSnapshot")
	}
	s, err := Open(dir, 1<<20)
	if err != nil {
		fmt.Println("open failed:", err)
		os.Exit(2)
	}
	p := testPage(t)
	filler := strings.Repeat("x", 200<<10) // a large record widens the window for a mid-write kill
	for i := 0; ; i++ {
		if _, err := s.Save(p, render.Result{HTML: fmt.Sprintf("<h1>v%d</h1>%s", i, filler), Status: 200, FinalURL: p.URL}); err != nil {
			fmt.Println("save failed:", err)
			os.Exit(2)
		}
		if i == 0 {
			fmt.Println("ready")
		}
	}
}

// The main storage promise: killing the process at any moment during a save
// leaves either the old or the new complete snapshot, never a damaged one.
func TestKilledWriterNeverCorruptsSnapshot(t *testing.T) {
	if testing.Short() {
		t.Skip("starts and kills child processes")
	}
	dir := t.TempDir()
	p := testPage(t)
	const rounds = 60
	versions := map[string]bool{}
	partial := 0
	for round := range rounds {
		cmd := exec.Command(os.Args[0], "-test.run=^TestKilledWriterHelper$", "-test.count=1")
		cmd.Env = append(os.Environ(), killHelperEnv+"="+dir)
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		ready := make(chan string, 1)
		go func() {
			line, _ := bufio.NewReader(out).ReadString('\n')
			ready <- line
		}()
		select {
		case line := <-ready:
			if strings.TrimSpace(line) != "ready" {
				cmd.Process.Kill()
				cmd.Wait()
				t.Fatalf("round %d: helper did not start: %q", round, line)
			}
		case <-time.After(20 * time.Second):
			cmd.Process.Kill()
			cmd.Wait()
			t.Fatalf("round %d: helper timed out", round)
		}
		time.Sleep(time.Duration(rand.IntN(150)) * time.Millisecond)
		if err = cmd.Process.Kill(); err != nil {
			t.Fatal(err)
		}
		cmd.Wait()

		s, err := Open(dir, 1<<20)
		if err != nil {
			t.Fatalf("round %d: reopen after kill: %v", round, err)
		}
		record, err := s.Load(p)
		if err != nil {
			t.Fatalf("round %d: snapshot unreadable after kill: %v", round, err)
		}
		head, _, _ := strings.Cut(record.Result.HTML, "</h1>")
		if !strings.HasPrefix(head, "<h1>v") {
			t.Fatalf("round %d: unexpected content %.40q", round, record.Result.HTML)
		}
		versions[head] = true
		removed, err := s.CleanupTemps()
		if err != nil {
			t.Fatalf("round %d: temp cleanup: %v", round, err)
		}
		partial += removed // a leftover temp file means the kill landed mid-save
		s.Close()
	}
	t.Logf("RESULT kill_rounds=%d", rounds)
	t.Logf("RESULT kill_corrupt_snapshots=0")
	t.Logf("RESULT kill_distinct_versions_seen=%d", len(versions))
	t.Logf("RESULT kills_during_a_write=%d", partial)
}
