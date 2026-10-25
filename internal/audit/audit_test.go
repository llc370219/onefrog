package audit

import "testing"

func TestDecide(t *testing.T) {
	allow := []string{"df -h /", "systemctl reload nginx", "ls /var/log", "rm file.txt"}
	for _, cmd := range allow {
		if d := Decide(cmd); !d.Allow {
			t.Fatalf("%s should be allowed, got %s", cmd, d.Reason)
		}
	}
	block := []string{"rm -rf /var/log/nginx/*", "rm -fr /", "mkfs.ext4 /dev/sdb", "dd if=/dev/zero of=/dev/sda", "reboot", "shutdown -h now"}
	for _, cmd := range block {
		if d := Decide(cmd); d.Allow {
			t.Fatalf("%s should be blocked", cmd)
		}
	}
}
