package maintenance

import "testing"

func TestPauseWaitsForWritersAndSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	g, err := New(dir)
	if err != nil {
		t.Fatal(err)
	}
	done, err := g.Enter()
	if err != nil {
		t.Fatal(err)
	}
	if err = g.Pause(); err != ErrBusy {
		t.Fatal("live writer was not protected", err)
	}
	done()
	if err = g.Pause(); err != nil {
		t.Fatal(err)
	}
	if _, err = g.Enter(); err != ErrPaused {
		t.Fatal("paused gate admitted writer")
	}
	reloaded, err := New(dir)
	if err != nil || !reloaded.Paused() {
		t.Fatal("pause did not persist")
	}
	if err = reloaded.Resume(); err != nil {
		t.Fatal(err)
	}
	done, err = reloaded.Enter()
	if err != nil {
		t.Fatal(err)
	}
	done()
}
