package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"time"

	"go.bug.st/serial"
	"golang.org/x/sync/errgroup"
)

// ISO8601Time utility
type ISO8601Time time.Time

// ISO8601 date time format
const ISO8601 = `2006-01-02T15:04:05.000Z07:00`

// MarshalJSON interface function
func (t ISO8601Time) MarshalJSON() ([]byte, error) {
	return json.Marshal(time.Time(t).Format(ISO8601))
}

// Data - the data
type Data struct {
	CO2         int64       `json:"co2"`
	Humidity    float64     `json:"humidity"`
	Temperature float64     `json:"temperature"`
	Timestamp   ISO8601Time `json:"timestamp"`
}

func prepareDevice(ctx context.Context, p serial.Port, s *bufio.Scanner) error {
	log.Println("Prepare device...:")
	for _, c := range []string{"STP", "ID?", "STA"} {
		log.Printf(" %v", c)
		if _, err := p.Write([]byte(c + "\r\n")); err != nil {
			return err
		}
		time.Sleep(time.Millisecond * 100) // wait
		for s.Scan() {
			select {
			case <-ctx.Done():
				return errors.New("context canceled")
			default:
				// do nothing
			}
			t := s.Text()
			if t[:2] == `OK` {
				break
			} else if t[:2] == `NG` {
				return fmt.Errorf(" command `%v` failed", c)
			}
		}
	}
	log.Println(" OK.")
	return nil
}

func correctHumidity(h float64, t float64) float64 {
	t1 := correctTemperature(t)
	return h *
		math.Pow(10.0, 7.5*t/(t+237.3)) /
		math.Pow(10.0, 7.5*t1/(t1+237.3))
}

func correctTemperature(t float64) float64 {
	return t - 4.5
}

func run() error {
	var device string
	flag.StringVar(&device, "device", "", "device to use")
	flag.Parse()

	if device == "" {
		return errors.New("device is required")
	}

	// trap SIGINT
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	var latest *Data = nil

	eg := errgroup.Group{}
	eg.Go(func() error {
		port, err := serial.Open(device, &serial.Mode{
			BaudRate: 115200,
			DataBits: 8,
			StopBits: serial.OneStopBit,
			Parity:   serial.NoParity,
		})
		if err != nil {
			return fmt.Errorf("failed to open port: %w", err)
		}
		defer func() {
			port.Write([]byte("STP\r\n"))
			time.Sleep(100 * time.Millisecond)
			port.Close()
		}()

		port.SetReadTimeout(time.Second * 10)
		s := bufio.NewScanner(port)
		s.Split(bufio.ScanLines)

		if err := prepareDevice(ctx, port, s); err != nil {
			return err
		}

		// reader (main)
		re := regexp.MustCompile(`CO2=(\d+),HUM=([0-9\.]+),TMP=([0-9\.-]+)`)
	scan:
		for s.Scan() {
			select {
			case <-ctx.Done():
				break scan
			default:
				// do nothing
			}
			now := time.Now()
			text := s.Text()
			m := re.FindAllStringSubmatch(text, -1)
			if len(m) > 0 {
				co2, _ := strconv.ParseInt(m[0][1], 10, 64)
				h, _ := strconv.ParseFloat(m[0][2], 64)
				t, _ := strconv.ParseFloat(m[0][3], 64)
				latest = &Data{
					CO2:         co2,
					Humidity:    correctHumidity(h, t),
					Temperature: correctTemperature(t),
					Timestamp:   ISO8601Time(now),
				}
			} else if text[:6] == `OK STP` {
				break // exit 0
			} else {
				log.Printf("Read unmatched string: %v\n", text)
			}
		}
		if err := s.Err(); err != nil {
			return fmt.Errorf("scanner error: %w", err)
		}

		log.Println("Reader stopped.")

		return nil
	})

	eg.Go(func() error {
		mux := http.NewServeMux()

		mux.HandleFunc("/data", func(w http.ResponseWriter, r *http.Request) {
			if latest == nil {
				http.Error(w, "no data", http.StatusServiceUnavailable)
				return
			}

			b, err := json.Marshal(latest)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}

			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write(b)
		})

		s := &http.Server{
			Addr:    "localhost:8080",
			Handler: mux,
		}

		go func() {
			<-ctx.Done()
			log.Println("Shutting down HTTP server...")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			s.Shutdown(ctx)
			log.Println("HTTP server stopped.")
		}()

		if err := s.ListenAndServe(); err != http.ErrServerClosed {
			return err
		}
		return nil
	})

	return eg.Wait()
}

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
