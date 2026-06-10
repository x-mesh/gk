package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/x-mesh/gk/internal/git"
)

func TestCheckRemoteSymmetry(t *testing.T) {
	ctx := context.Background()

	t.Run("no remotes is PASS", func(t *testing.T) {
		fake := &git.FakeRunner{Responses: map[string]git.FakeResponse{
			"remote": {Stdout: ""},
		}}
		c := checkRemoteSymmetry(ctx, fake)
		if c.Status != statusPass || !strings.Contains(c.Detail, "no remotes") {
			t.Fatalf("got %+v", c)
		}
	})

	t.Run("symmetric remote is PASS", func(t *testing.T) {
		fake := &git.FakeRunner{Responses: map[string]git.FakeResponse{
			"remote":                         {Stdout: "origin\n"},
			"config --get remote.origin.url": {Stdout: "git@github.com:a/b.git\n"},
			// no pushurl entries → git config --get-all errors
			"config --get-all remote.origin.pushurl": {Err: context.DeadlineExceeded},
		}}
		c := checkRemoteSymmetry(ctx, fake)
		if c.Status != statusPass {
			t.Fatalf("got %+v", c)
		}
	})

	t.Run("extra pushurl is WARN with remedy", func(t *testing.T) {
		fake := &git.FakeRunner{Responses: map[string]git.FakeResponse{
			"remote":                                 {Stdout: "origin\n"},
			"config --get remote.origin.url":         {Stdout: "git@github.com:JINWOO-J/deployer-bot.git\n"},
			"config --get-all remote.origin.pushurl": {Stdout: "git@github.com:JINWOO-J/deployer-bot.git\ngit@github.com-nobody-j:42tape/deploy-bot\n"},
		}}
		c := checkRemoteSymmetry(ctx, fake)
		if c.Status != statusWarn {
			t.Fatalf("want WARN, got %+v", c)
		}
		if !strings.Contains(c.Detail, "42tape/deploy-bot") || !strings.Contains(c.Detail, "never comes down") {
			t.Errorf("detail must name the push-only URL and the consequence: %s", c.Detail)
		}
		if !strings.Contains(c.Fix, "gk pull --from") {
			t.Errorf("fix must point at pull --from: %s", c.Fix)
		}
	})

	t.Run("pushurl identical to fetch is PASS", func(t *testing.T) {
		fake := &git.FakeRunner{Responses: map[string]git.FakeResponse{
			"remote":                                 {Stdout: "origin\n"},
			"config --get remote.origin.url":         {Stdout: "git@github.com:a/b.git\n"},
			"config --get-all remote.origin.pushurl": {Stdout: "git@github.com:a/b.git\n"},
		}}
		c := checkRemoteSymmetry(ctx, fake)
		if c.Status != statusPass {
			t.Fatalf("got %+v", c)
		}
	})
}
