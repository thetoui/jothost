package collectors

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// The panel runs a second nginx and a second PHP-FPM master for its own
// applications, and the kernel calls them "nginx" and "php-fpm84" like any
// other. Before these, stopping the nginx that serves websites left the panel
// reporting nginx as running while every site on the host was down — the worst
// kind of wrong answer, because it is the one an operator checks first.

// fakeProc builds a /proc tree. Each process is pid, comm, ppid and cmdline.
type fakeProcess struct {
	pid     int
	comm    string
	ppid    int
	cmdline string
}

func fakeProc(t *testing.T, processes []fakeProcess) string {
	t.Helper()
	root := t.TempDir()

	for _, process := range processes {
		dir := filepath.Join(root, fmt.Sprint(process.pid))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("create %s: %v", dir, err)
		}
		write := func(name, content string) {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
				t.Fatalf("write %s/%s: %v", dir, name, err)
			}
		}
		write("comm", process.comm+"\n")
		// The real file is NUL-separated; a space-separated one would let a
		// bug that splits on spaces pass.
		write("cmdline", nulSeparated(process.cmdline))
		// The name sits in parentheses and the parent is two fields later.
		write("stat", fmt.Sprintf("%d (%s) S %d 0 0 0 -1 0", process.pid, process.comm, process.ppid))
	}
	return root
}

func nulSeparated(cmdline string) string {
	out := make([]byte, 0, len(cmdline)+1)
	for i := 0; i < len(cmdline); i++ {
		if cmdline[i] == ' ' {
			out = append(out, 0)
			continue
		}
		out = append(out, cmdline[i])
	}
	return string(append(out, 0))
}

func TestTheHostServiceIsFoundWhenItIsTheOnlyOne(t *testing.T) {
	root := fakeProc(t, []fakeProcess{
		{pid: 100, comm: "nginx", ppid: 1, cmdline: "nginx: master process /usr/sbin/nginx -c /etc/nginx/nginx.conf"},
		{pid: 101, comm: "nginx", ppid: 100, cmdline: "nginx: worker process"},
	})
	collector := New(Options{ProcRoot: root})

	found := collector.FindByName([]string{"nginx"})
	if found["nginx"] != 100 {
		t.Fatalf("expected the master at pid 100, got %v", found)
	}
}

func TestThePanelOwnNginxIsNotTheHostNginx(t *testing.T) {
	// The state that produced the wrong answer: the website nginx stopped, the
	// panel's own still serving phpMyAdmin.
	root := fakeProc(t, []fakeProcess{
		{pid: 200, comm: "nginx", ppid: 1,
			cmdline: "nginx: master process /usr/sbin/nginx -p /etc/jothost/web -c /etc/jothost/web/nginx.conf"},
		{pid: 201, comm: "nginx", ppid: 200, cmdline: "nginx: worker process"},
		{pid: 202, comm: "nginx", ppid: 200, cmdline: "nginx: worker process"},
	})
	collector := New(Options{ProcRoot: root})

	if found := collector.FindByName([]string{"nginx"}); len(found) != 0 {
		t.Fatalf("the panel's own nginx was reported as the host's: %v", found)
	}
}

func TestThePanelWorkersAreExcludedToo(t *testing.T) {
	// A worker's command line says only "nginx: worker process" — there is
	// nothing in it to tell one instance's workers from another's. Excluding
	// the master alone would leave the workers making a stopped nginx look
	// like a running one just as surely.
	root := fakeProc(t, []fakeProcess{
		{pid: 300, comm: "nginx", ppid: 1,
			cmdline: "nginx: master process /usr/sbin/nginx -p /etc/jothost/web -c /etc/jothost/web/nginx.conf"},
		{pid: 301, comm: "nginx", ppid: 300, cmdline: "nginx: worker process"},
	})
	collector := New(Options{ProcRoot: root})

	found := collector.FindByName([]string{"nginx"})
	if _, present := found["nginx"]; present {
		t.Fatalf("a panel worker was reported as the host's nginx: %v", found)
	}
}

func TestBothStacksRunningReportsTheHostOne(t *testing.T) {
	root := fakeProc(t, []fakeProcess{
		// Deliberately the lower pid, so a naive "lowest pid wins" picks the
		// wrong one.
		{pid: 400, comm: "nginx", ppid: 1,
			cmdline: "nginx: master process /usr/sbin/nginx -p /etc/jothost/web -c /etc/jothost/web/nginx.conf"},
		{pid: 401, comm: "nginx", ppid: 400, cmdline: "nginx: worker process"},
		{pid: 500, comm: "nginx", ppid: 1,
			cmdline: "nginx: master process /usr/sbin/nginx -c /etc/nginx/nginx.conf"},
		{pid: 501, comm: "nginx", ppid: 500, cmdline: "nginx: worker process"},
	})
	collector := New(Options{ProcRoot: root})

	found := collector.FindByName([]string{"nginx"})
	if found["nginx"] != 500 {
		t.Fatalf("expected the host's master at 500, got %v", found)
	}
}

func TestThePanelPHPMasterIsNotTheWebsitesPHP(t *testing.T) {
	// The same failure in the other half of the stack: the websites' PHP down,
	// the panel's own up, and the panel claiming PHP-FPM is running.
	root := fakeProc(t, []fakeProcess{
		{pid: 600, comm: "php-fpm84", ppid: 1,
			cmdline: "php-fpm: master process (/etc/jothost/web/php-fpm.conf)"},
		{pid: 601, comm: "php-fpm84", ppid: 600, cmdline: "php-fpm: pool jothost-pma"},
	})
	collector := New(Options{ProcRoot: root})

	if found := collector.FindByName([]string{"php-fpm84"}); len(found) != 0 {
		t.Fatalf("the panel's PHP master was reported as the websites': %v", found)
	}
}

func TestAnUnrelatedProcessIsUnaffected(t *testing.T) {
	root := fakeProc(t, []fakeProcess{
		{pid: 700, comm: "nginx", ppid: 1,
			cmdline: "nginx: master process /usr/sbin/nginx -p /etc/jothost/web -c /etc/jothost/web/nginx.conf"},
		{pid: 800, comm: "sshd", ppid: 1, cmdline: "/usr/sbin/sshd -D"},
	})
	collector := New(Options{ProcRoot: root})

	found := collector.FindByName([]string{"nginx", "sshd"})
	if found["sshd"] != 800 {
		t.Fatalf("an unrelated daemon was excluded: %v", found)
	}
}
