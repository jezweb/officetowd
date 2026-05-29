package service

import "strings"

import "testing"

func TestPlanForDarwin(t *testing.T) {
	p := PlanFor("darwin", "/usr/local/bin/officetowd", "/Users/jez")
	if !p.Supported {
		t.Fatal("darwin should be supported")
	}
	if p.UnitPath != "/Users/jez/Library/LaunchAgents/au.jezweb.officetowd.plist" {
		t.Errorf("unit path: %s", p.UnitPath)
	}
	for _, want := range []string{"<string>/usr/local/bin/officetowd</string>", "<string>start</string>", "RunAtLoad", "KeepAlive", "au.jezweb.officetowd"} {
		if !strings.Contains(p.Content, want) {
			t.Errorf("plist missing %q", want)
		}
	}
	if p.LoadCmds[len(p.LoadCmds)-1][0] != "launchctl" {
		t.Errorf("expected launchctl load, got %v", p.LoadCmds)
	}
}

func TestPlanForLinux(t *testing.T) {
	p := PlanFor("linux", "/home/u/.local/bin/officetowd", "/home/u")
	if !p.Supported {
		t.Fatal("linux should be supported")
	}
	if p.UnitPath != "/home/u/.config/systemd/user/officetowd.service" {
		t.Errorf("unit path: %s", p.UnitPath)
	}
	for _, want := range []string{"ExecStart=/home/u/.local/bin/officetowd start", "Restart=always", "WantedBy=default.target"} {
		if !strings.Contains(p.Content, want) {
			t.Errorf("unit missing %q", want)
		}
	}
}

func TestPlanForUnsupported(t *testing.T) {
	if PlanFor("plan9", "/x", "/home").Supported {
		t.Error("plan9 should be unsupported")
	}
}
