package harness

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// QueueDepth reports messages_ready and messages_unacknowledged for a
// RabbitMQ queue via rabbitmqctl inside the compose container.
func QueueDepth(queue string) (ready, unacked int, err error) {
	root, err := RepoRoot()
	if err != nil {
		return 0, 0, err
	}
	args := []string{
		"compose", "--env-file", filepath.Join(root, "loadtest", ".env"),
		"-f", filepath.Join(root, "docker-compose.yml"),
		"exec", "-T", "rabbitmq", "rabbitmqctl",
		"list_queues", "name", "messages_ready", "messages_unacknowledged",
	}
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		return 0, 0, fmt.Errorf("rabbitmqctl failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 3 || fields[0] != queue {
			continue
		}
		ready, _ = strconv.Atoi(fields[1])
		unacked, _ = strconv.Atoi(fields[2])
		return ready, unacked, nil
	}
	return 0, 0, fmt.Errorf("queue %q not found", queue)
}
