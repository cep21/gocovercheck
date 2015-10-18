package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/tools/cover"
)

type gocovercheck struct {
	verbose          bool
	race             bool
	requiredCoverage float64
	timeout          time.Duration
	parallel         int64
	coverprofile     string
	stdout           string
	stderr           string

	logout  io.Writer
	cmdArgs []string

	cmdRun func(*exec.Cmd) error
}

type wrappedError struct {
	msg string
	err error
}

func (w *wrappedError) Error() string {
	return fmt.Sprintf("%s: %s", w.msg, w.err)
}

func wraperr(err error, fmtString string, args ...interface{}) error {
	return &wrappedError{fmt.Sprintf(fmtString, args...), err}
}

var mainGoCoverCheck = gocovercheck{
	cmdRun: runCmd,
	logout: ioutil.Discard,
}

func runCmd(c *exec.Cmd) error {
	return c.Run()
}

func init() {
	flag.Float64Var(&mainGoCoverCheck.requiredCoverage, "required_coverage", 0, "Sets the required coverage for non zero error code.")
	flag.BoolVar(&mainGoCoverCheck.race, "race", false, "Set race detection")
	flag.DurationVar(&mainGoCoverCheck.timeout, "timeout", 0, "Timeout testing")
	flag.Int64Var(&mainGoCoverCheck.parallel, "parallel", 0, "Parallel testing")
	flag.StringVar(&mainGoCoverCheck.coverprofile, "coverprofile", "", "Coverage profile output")
	flag.StringVar(&mainGoCoverCheck.stdout, "stdout", "", "File to pipe stdout to.  - means stdout")
	flag.StringVar(&mainGoCoverCheck.stderr, "stderr", "", "File to pipe stderr to.  - means stderr")

	flag.BoolVar(&mainGoCoverCheck.verbose, "verbose", false, "If set, will send to stderr verbose logging out")
}

func main() {
	flag.Parse()
	mainGoCoverCheck.cmdArgs = flag.Args()
	if mainGoCoverCheck.verbose {
		mainGoCoverCheck.logout = os.Stderr
	}
	err := mainGoCoverCheck.main()
	if err != nil {
		fmt.Fprintf(os.Stdout, "%s\n", err.Error())
		os.Exit(1)
	}
}

type discardWriteCloser struct {
	io.Writer
	io.Closer
}

var discardWriteCloserStruct = &discardWriteCloser{
	Writer: ioutil.Discard,
	Closer: ioutil.NopCloser(&bytes.Buffer{}),
}

func forFile(filename string, dash io.WriteCloser) (io.WriteCloser, error) {
	if filename == "-" {
		return dash, nil
	}
	if filename == "" {
		return discardWriteCloserStruct, nil
	}
	return os.Create(filename)
}

func (g *gocovercheck) setupBasicArgs() []string {
	args := make([]string, 0, 5)
	if g.race {
		args = append(args, "-race")
	}
	if g.timeout.Nanoseconds() > 0 {
		args = append(args, "-timeout", g.timeout.String())
	}
	if g.parallel > 0 {
		args = append(args, "-parallel", strconv.FormatInt(g.parallel, 10))
	}
	args = append(args, "-coverprofile", g.coverprofile)
	return args
}

func (g *gocovercheck) main() error {
	l := log.New(g.logout, "[gocovercheck]", 0)
	cmd := "go"
	args := []string{"test", "-covermode", "atomic"}
	if g.coverprofile == "" {
		coverProfileFile, err := ioutil.TempFile("", "gocovercheck")
		if err != nil {
			return wraperr(err, "Cannot create gocovercheck temp file")
		}
		defer func() {
			logIfErr(l, os.Remove(coverProfileFile.Name()), "Unable to remove cover profile file.")
		}()
		if err := coverProfileFile.Close(); err != nil {
			return wraperr(err, "Unable to close originally opened cover file")
		}
		g.coverprofile = coverProfileFile.Name()

		l.Printf("Setting coverprofile to %s\n", g.coverprofile)
	}

	args = append(args, g.setupBasicArgs()...)

	stdout, err := forFile(g.stdout, os.Stdout)
	if err != nil {
		return wraperr(err, "Cannot open stdout pipe file")
	}
	if stdout != os.Stdout {
		defer func() {
			logIfErr(l, stdout.Close(), "Unable to finish closing stdout")
		}()
	}
	stderr, err := forFile(g.stderr, os.Stderr)
	if err != nil {
		return wraperr(err, "Cannot open stderr pipe file")
	}
	if stderr != os.Stderr {
		defer func() {
			logIfErr(l, stderr.Close(), "Unable to close stderr")
		}()
	}
	args = append(args, g.cmdArgs...)
	e := exec.Command(cmd, args...)
	e.Stdout = stdout
	e.Stderr = stderr
	l.Printf("Running cmd=[%s] args=[%v]\n", cmd, strings.Join(args, ", "))
	return g.runCmd(l, e)
}

func (g *gocovercheck) runCmd(l *log.Logger, e *exec.Cmd) error {
	if err := g.cmdRun(e); err != nil {
		return wraperr(err, "cannot run command")
	}
	l.Printf("Finished running command\n")
	coverage, err := calculateCoverage(g.coverprofile)
	if err != nil {
		return wraperr(err, "cannot load coverage profile file")
	}
	l.Printf("Calculated coverage %.2f\n", coverage)
	if coverage+.001 < g.requiredCoverage {
		return fmt.Errorf("%s::warning:Code coverage %.3f less than required %f", guessPackageName(l, g.coverprofile), coverage, g.requiredCoverage)
	}
	return nil
}

func logIfErr(l *log.Logger, err error, msg string, args ...interface{}) {
	if err != nil {
		l.Printf("%s: %s", fmt.Sprintf(msg, args...), err.Error())
	}
}

var defaultPackageName = ""

func guessPackageName(l *log.Logger, coverprofile string) string {
	f, err := os.Open(coverprofile)
	if err != nil {
		return defaultPackageName
	}
	defer func() {
		logIfErr(l, f.Close(), "Unable to close opened file about coverprofile")
	}()

	scanner := bufio.NewScanner(f)
	if !scanner.Scan() || !scanner.Scan() {
		return defaultPackageName
	}
	nextLine := scanner.Text()
	lineParts := strings.Split(nextLine, ":")
	if len(lineParts) < 2 {
		return defaultPackageName
	}
	return filepath.Dir(lineParts[0])
}

func calculateCoverage(coverprofile string) (float64, error) {
	profiles, err := cover.ParseProfiles(coverprofile)
	if err != nil {
		return 0.0, wraperr(err, "cannot parse coverage profile file %s", coverprofile)
	}
	total := 0
	covered := 0
	for _, profile := range profiles {
		for _, block := range profile.Blocks {
			total += block.NumStmt
			if block.Count > 0 {
				covered += block.NumStmt
			}
		}
	}
	if total == 0 {
		return 0.0, nil
	}
	return float64(covered) / float64(total) * 100, nil
}
