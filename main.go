package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up kubectl port-forward with piped stdout.
	kubectl := exec.CommandContext(ctx, "kubectl", append([]string{"port-forward"}, os.Args[1:]...)...)
	kubectl.Stderr = os.Stderr
	stdout, err := kubectl.StdoutPipe()
	if err != nil {
		fmt.Fprintf(os.Stderr, "attaching to kubectl stderr: %s\n", err)
		os.Exit(1)
	}

	// Wire up kubectl's stdout to be written to os.Stdout and read from r.
	r, w := io.Pipe()
	go io.Copy(os.Stdout, io.TeeReader(stdout, w))

	// Read the URLs from r.
	var urls []string
	var ulock sync.RWMutex
	go func() {
		newURLs := make(chan string)
		go readURLs(r, newURLs)
		for u := range newURLs {
			ulock.Lock()
			urls = append(urls, u)
			ulock.Unlock()
		}
	}()

	// Health-check the URLs every now and again.
	go func() {
		c := &http.Client{
			Transport: http.DefaultTransport,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		}

		for {
			time.Sleep(3 * time.Second)
			ulock.RLock()
			for _, u := range urls {
				if err := httpHealthCheck(c, u); err != nil {
					fmt.Fprintf(os.Stderr, "health check failed: %s\n", err)
					cancel()
				}
				break // Quit early: the first is representative of them all.
			}
			ulock.RUnlock()
		}
	}()

	// Run kubectl port-forward.
	if err := kubectl.Run(); err != nil {
		if ctx.Err() == nil {
			fmt.Fprintf(os.Stderr, "kubectl: %s\n", err)
		}
		os.Exit(1)
	}
}

// readURLs scans the lines from r for port-forwarding mentions and writes them
// to channel u.
func readURLs(r io.Reader, u chan<- string) error {
	s := bufio.NewScanner(r)
	for s.Scan() {
		l := s.Text()
		if !strings.HasPrefix(l, "Forwarding from ") {
			continue
		}
		p := strings.Index(l, " -> ")
		if p == -1 {
			continue
		}
		u <- "http://" + l[16:p]
	}
	return s.Err()
}

// httpHealthCheck returns any error received attempting to connect to the given URL.
func httpHealthCheck(c *http.Client, u string) error {
	r, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return err
	}

	_, err = c.Do(r)
	return err
}
