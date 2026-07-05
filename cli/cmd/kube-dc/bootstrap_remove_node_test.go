package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestBootstrapRemoveNode_RequiresNodeName(t *testing.T) {
	var buf bytes.Buffer
	repo := ""
	cmd := bootstrapRemoveNodeCmd(&repo)
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs(nil) // no node-name
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "node-name") {
		t.Errorf("want a required-node-name error, got %v", err)
	}
}

func TestBootstrapRemoveNode_TooManyArgs(t *testing.T) {
	var buf bytes.Buffer
	repo := ""
	cmd := bootstrapRemoveNodeCmd(&repo)
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"a", "b"}) // MaximumNArgs(1)
	if err := cmd.Execute(); err == nil {
		t.Error("two positional args should be rejected")
	}
}
