package main

import (
	"flag"
	"fmt"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// Global variables for tracking state
var (
	sentTimeTracker     map[int]map[string]time.Time
	sentTimeTrackerLock sync.Mutex
	nodeName            string
)

type PingResult struct {
	Target      string
	RTT         time.Duration
	Success     bool
	Err         error
	ICMPID      int
	ICMPSeq     int
	Timestamp   time.Time // When response received
	SentTime    time.Time // When request was sent
	LoggedError bool      // Flag to indicate if error has already been logged
}

type PingStats struct {
	Target     string
	Sent       int
	Received   int
	MinRTT     time.Duration
	MaxRTT     time.Duration
	AvgRTT     time.Duration
	LastResult *PingResult
	mux        sync.Mutex
}

func (ps *PingStats) AddResult(result PingResult) {
	ps.mux.Lock()
	defer ps.mux.Unlock()

	ps.LastResult = &result
	if result.Success {
		ps.Received++
		if ps.MinRTT == 0 || result.RTT < ps.MinRTT {
			ps.MinRTT = result.RTT
		}
		if result.RTT > ps.MaxRTT {
			ps.MaxRTT = result.RTT
		}
		// Update running average
		if ps.Received == 1 {
			ps.AvgRTT = result.RTT
		} else {
			ps.AvgRTT = time.Duration((float64(ps.AvgRTT)*float64(ps.Received-1) + float64(result.RTT)) / float64(ps.Received))
		}
	}
	ps.Sent++
}

func main() {
	// Parse command-line arguments
	intervalFlag := flag.Float64("i", 1.0, "Interval between ping batches in seconds")
	timeoutFlag := flag.Float64("t", 2.0, "Timeout for ping responses in seconds")
	// Just parse -v flag for backwards compatibility
	_ = flag.Bool("v", false, "Verbose output")
	
	// Setup logging for stdout and stderr - these will be used consistently throughout the code
	flag.Parse()

	targets := flag.Args()
	if len(targets) == 0 {
		fmt.Println("Usage: go-pinger [-i interval] [-t timeout] [-v] target1 [target2...]")
		os.Exit(1)
	}

	interval := time.Duration(*intervalFlag * float64(time.Second))
	timeout := time.Duration(*timeoutFlag * float64(time.Second))

	// Create ICMP connection
	conn, err := icmp.ListenPacket("ip4:icmp", "0.0.0.0")
	if err != nil {
		fmt.Printf("Error creating ICMP connection: %v\n", err)
		os.Exit(1)
	}
	defer conn.Close()

	// Initialize statistics for each target
	statsMap := make(map[string]*PingStats)
	for _, target := range targets {
		statsMap[target] = &PingStats{Target: target}
	}

	// Channel for results
	resultChan := make(chan PingResult, len(targets))

	// Create a channel for handling interrupt signal
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// Channel to signal goroutine termination
	done := make(chan struct{})

	// Start ticker for periodic pings
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Start a goroutine to handle incoming packets
	responseMap := make(map[int]map[string]bool) // track expected responses by ICMP ID
	var responseMux sync.Mutex

	go func() {
		buf := make([]byte, 1500)
		for {
			// Check if we should terminate
			select {
			case <-done:
				return
			default:
				// Continue with normal operation
			}
			conn.SetReadDeadline(time.Now().Add(timeout))
			n, peer, err := conn.ReadFrom(buf)
			rxTime := time.Now()

			if err != nil {
				if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
					// Timeout is normal, just continue
					continue
				}
				fmt.Fprintf(os.Stderr, "ERROR: Error reading ICMP packet: %v\n", err)
				continue
			}

			msg, err := icmp.ParseMessage(1 /* IPv4 */, buf[:n])
			if err != nil {
				fmt.Fprintf(os.Stderr, "ERROR: Error parsing ICMP message: %v\n", err)
				continue
			}

			if msg.Type != ipv4.ICMPTypeEchoReply {
				continue
			}

			echo := msg.Body.(*icmp.Echo)
			icmpID := echo.ID
			icmpSeq := echo.Seq
			target := peer.String()

			// Check if this is an expected response
			responseMux.Lock()
			if targetMap, ok := responseMap[icmpID]; ok {
				if _, exists := targetMap[target]; exists {
					// Get timestamp from the payload
					var txTime time.Time
					if len(echo.Data) >= 8 {
						txTime = time.Unix(0, int64(byteArrayToUint64(echo.Data[:8])))
					}
					
					// Get sent time from our tracker if available
					sentTimeTrackerLock.Lock()
					sentTime := txTime
					if sentTimeMap, ok := sentTimeTracker[icmpID]; ok {
						if st, ok := sentTimeMap[target]; ok {
							sentTime = st
						}
					}
					sentTimeTrackerLock.Unlock()

					rtt := rxTime.Sub(txTime)
					result := PingResult{
						Target:    target,
						RTT:       rtt,
						Success:   true,
						ICMPID:    icmpID,
						ICMPSeq:   icmpSeq,
						Timestamp: rxTime,
						SentTime:  sentTime,
					}
					resultChan <- result

					// Remove from expected responses
					delete(targetMap, target)
				}
			}
			responseMux.Unlock()
		}
	}()

	// Initialize the global sent time tracker
	sentTimeTracker = make(map[int]map[string]time.Time)
	
	// Get node name from environment variable
	nodeName = os.Getenv("NODE_NAME")
	if nodeName == "" {
		nodeName = "unknown"
	}

	// Main loop
	batchID := rand.Intn(0xFFFF) // Start with a random ID
	tick := time.NewTicker(interval)
	defer tick.Stop()

	fmt.Fprintf(os.Stdout, "Starting pinger on node [%s]. Press Ctrl+C to stop.\n", nodeName)
	for {
		select {
		case <-tick.C:
			// Create a new batch with a common ID
			batchID = (batchID + 1) % 0xFFFF
			if batchID == 0 { // Avoid ID 0
				batchID = 1
			}

			// Register expected responses and initialize sent time tracking
			responseMux.Lock()
			responseMap[batchID] = make(map[string]bool)
			responseMux.Unlock()
			
			sentTimeTrackerLock.Lock()
			sentTimeTracker[batchID] = make(map[string]time.Time)
			sentTimeTrackerLock.Unlock()
			
			responseMux.Lock()
			for _, target := range targets {
				responseMap[batchID][target] = true
			}
			responseMux.Unlock()

			// Launch goroutines for each target
			var wg sync.WaitGroup
			for _, target := range targets {
				wg.Add(1)
				go func(target string) {
					defer wg.Done()

					// Resolve target IP
					ipAddr, err := net.ResolveIPAddr("ip4", target)
					if err != nil {
						resultChan <- PingResult{
							Target:    target,
							Success:   false,
							Err:       fmt.Errorf("could not resolve %s: %v", target, err),
							ICMPID:    batchID,
							Timestamp: time.Now(),
						}
						return
					}

					// Create ICMP echo request
					txTime := time.Now()
					timeBytes := uint64ToByteArray(uint64(txTime.UnixNano()))

					// Add some padding to make the packet a standard size
					padding := make([]byte, 56-len(timeBytes))
					for i := range padding {
						padding[i] = byte(i)
					}

					payload := append(timeBytes, padding...)

					msg := icmp.Message{
						Type: ipv4.ICMPTypeEcho,
						Code: 0,
						Body: &icmp.Echo{
							ID:   batchID,
							Seq:  1,
							Data: payload,
						},
					}

					// Marshal the message
					b, err := msg.Marshal(nil)
					if err != nil {
						resultChan <- PingResult{
							Target:    target,
							Success:   false,
							Err:       fmt.Errorf("failed to marshal ICMP message: %v", err),
							ICMPID:    batchID,
							Timestamp: txTime,
						}
						return
					}

					// Send the message
					if _, err := conn.WriteTo(b, ipAddr); err != nil {
						resultChan <- PingResult{
							Target:    target,
							Success:   false,
							Err:       fmt.Errorf("failed to send ICMP message: %v", err),
							ICMPID:    batchID,
							Timestamp: txTime,
							SentTime:  txTime,
						}
						return
					}
					
					// Record sent time
					sentTimeTrackerLock.Lock()
					if sentTimeMap, ok := sentTimeTracker[batchID]; ok {
						sentTimeMap[target] = txTime
					}
					sentTimeTrackerLock.Unlock()

					// No longer print sent messages - we'll print a single line when we get a response
				}(target)
			}

			// Wait for all sends to complete
			wg.Wait()

			// Schedule timeout for this batch
			go func(currentBatchID int) {
				time.Sleep(timeout)
				responseMux.Lock()
				if targetMap, ok := responseMap[currentBatchID]; ok {
					// Generate timeout events for any targets that didn't respond
					for target := range targetMap {
						// Find the sent time for this target
						sentTimeTrackerLock.Lock()
						sentTime := time.Time{}
						if sentTimeMap, ok := sentTimeTracker[currentBatchID]; ok {
							if st, ok := sentTimeMap[target]; ok {
								sentTime = st
							}
						}
						sentTimeTrackerLock.Unlock()
						
						now := time.Now()
						
						// Create timeout result
						result := PingResult{
							Target:      target,
							Success:     false,
							Err:         fmt.Errorf("timeout"),
							ICMPID:      currentBatchID,
							Timestamp:   now,
							SentTime:    sentTime,
							LoggedError: true, // Mark as already logged
						}
						
						// Print a single line with all information
						// Add ERROR: prefix for Grafana to properly colorize as red
						fmt.Fprintf(os.Stderr, "ERROR: [%s] FAILED sent to %s at %s ID %d after %.2fms: %v\n",
							nodeName,
							result.Target,
							result.SentTime.Format("15:04:05.000"),
							result.ICMPID,
							float64(now.Sub(sentTime).Microseconds())/1000.0,
							result.Err)
							
						// Still send to channel for stats
						resultChan <- result
					}
					delete(responseMap, currentBatchID)
				}
				// Clean up sent time tracker to avoid memory leaks
				sentTimeTrackerLock.Lock()
				for id := range sentTimeTracker {
					// Keep only the most recent batches (last 10)
					if id < batchID-10 {
						delete(sentTimeTracker, id)
					}
				}
				sentTimeTrackerLock.Unlock()
				responseMux.Unlock()
			}(batchID)

		case result := <-resultChan:
			// Update statistics
			if stats, ok := statsMap[result.Target]; ok {
				stats.AddResult(result)

				// Always show ping results
				if result.Success {
					fmt.Fprintf(os.Stdout, "[%s] SUCCESS sent to %s at %s ID %d time %.2fms\n",
						nodeName,
						result.Target,
						result.SentTime.Format("15:04:05.000"),
						result.ICMPID,
						float64(result.RTT.Microseconds())/1000.0)
				} else if !result.LoggedError {
					// Only print if not already logged
					fmt.Fprintf(os.Stderr, "ERROR: [%s] FAILED sent to %s at %s ID %d after %.2fms: %v\n",
						nodeName,
						result.Target,
						result.SentTime.Format("15:04:05.000"),
						result.ICMPID,
						float64(result.Timestamp.Sub(result.SentTime).Microseconds())/1000.0,
						result.Err)
				}
			}

		case <-sigChan:
			// Print final statistics
			fmt.Fprintln(os.Stdout, "\n--- Ping Statistics ---")
			for _, target := range targets {
				stats := statsMap[target]
				fmt.Fprintf(os.Stdout, "%s: %d packets transmitted, %d received, %.1f%% packet loss\n",
					target, stats.Sent, stats.Received,
					100.0-float64(stats.Received)/float64(stats.Sent)*100.0)

				if stats.Received > 0 {
					fmt.Fprintf(os.Stdout, "rtt min/avg/max = %.3f/%.3f/%.3f ms\n",
						float64(stats.MinRTT.Microseconds())/1000.0,
						float64(stats.AvgRTT.Microseconds())/1000.0,
						float64(stats.MaxRTT.Microseconds())/1000.0)
				}
			}
			// Signal the receiver goroutine to stop
			close(done)

			// Wait a moment for goroutine to finish
			time.Sleep(100 * time.Millisecond)

			// Explicitly close the connection
			conn.Close()
			return
		}
	}
}

// Helper functions to convert between uint64 and byte array
func uint64ToByteArray(val uint64) []byte {
	b := make([]byte, 8)
	for i := 0; i < 8; i++ {
		b[i] = byte(val >> (i * 8))
	}
	return b
}

func byteArrayToUint64(b []byte) uint64 {
	var val uint64
	for i := 0; i < 8 && i < len(b); i++ {
		val |= uint64(b[i]) << (i * 8)
	}
	return val
}
