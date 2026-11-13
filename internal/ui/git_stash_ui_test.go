package mcodeui

import (
	"context"
	"testing"
)

func TestGitStashToastAfterClose(t *testing.T) {
	s, ps, _ := gitPaneTestState(t)
	d := &gitStashDialog{s: s, threadID: ps.threadID, open: true}
	started, release := make(chan struct{}), make(chan struct{})
	d.runStashOp("贮藏", "已贮藏改动", func(context.Context) error { close(started); <-release; return nil })
	<-started
	d.closeStashDialog()
	close(release)
	gitPaneWait(t, func() bool { return recallTestToast(s) == "已贮藏改动" })
}
