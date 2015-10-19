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
	"strings"

	"encoding/json"

	"errors"

	"golang.org/x/tools/cover"
	"regexp"
)

type gocovercheck struct {
	verbose          bool
	requiredCoverage float64
	coverprofile     string
	testFlags        string
	stdout           string
	stderr           string
	dirout string

	logout  io.Writer
	log     *log.Logger
	cmdArgs []string

	bestGuessPackageName string

	cmdRun func(*exec.Cmd) error

	cleanupFunctions []func()
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
	flag.StringVar(&mainGoCoverCheck.testFlags, "testflags", "[]", "JSON array of cmd line flags to pass to test command")

	flag.StringVar(&mainGoCoverCheck.coverprofile, "coverprofile", "", "Coverage profile output")
	flag.StringVar(&mainGoCoverCheck.stdout, "stdout", "", "File to pipe stdout to.  - means stdout")
	flag.StringVar(&mainGoCoverCheck.stderr, "stderr", "", "File to pipe stderr to.  - means stderr")

	flag.StringVar(&mainGoCoverCheck.dirout, "dirout", "", "If set, will change stdout, stderr, and coverprofile to all coexist inside dirout with arg default params")

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
		fmt.Fprintf(os.Stdout, "%s::warning:%s\n", mainGoCoverCheck.bestGuessPackageName, err.Error())
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
	args = append(args, "-coverprofile", g.coverprofile)
	return args
}

func (g *gocovercheck) Close() error {
	for _, f := range g.cleanupFunctions {
		f()
	}
	return nil
}

func (g *gocovercheck) addCleanup(f func()) {
	g.cleanupFunctions = append(g.cleanupFunctions, f)
}

func (g *gocovercheck) setupTempCoverProfile() error {
	coverProfileFile, err2 := ioutil.TempFile("", "gocovercheck")
	if err2 != nil {
		return wraperr(err2, "Cannot create gocovercheck temp file")
	}
	g.addCleanup(func() {
		g.log.Printf("Removing file %s", coverProfileFile.Name())
		logIfErr(g.log, os.Remove(coverProfileFile.Name()), "Unable to remove cover profile file.")
	})
	if err2 := coverProfileFile.Close(); err2 != nil {
		return wraperr(err2, "Unable to close originally opened cover file")
	}
	g.coverprofile = coverProfileFile.Name()
	g.log.Printf("Setting coverprofile to %s\n", g.coverprofile)
	return nil
}

func (g *gocovercheck) setupRedirect(filename string, dash io.WriteCloser) (io.WriteCloser, error) {
	stdout, err := forFile(filename, dash)
	if err != nil {
		return nil, wraperr(err, "Cannot open pipe file")
	}
	if stdout != dash {
		g.addCleanup(func() {
			logIfErr(g.log, stdout.Close(), "Unable to finish closing out")
		})
	}
	return stdout, nil
}

var validFilenames = regexp.MustCompile("[^A-Za-z0-9\\._-]+")

func sanitizeForDirectory(s string) string {
	s = strings.TrimSpace(s)
	s = validFilenames.ReplaceAllString(s, "_")

	return s
}

func (g *gocovercheck) main() error {
	defer func() {
		logIfErr(mainGoCoverCheck.log, g.Close(), "Should never happen")
	}()
	g.log = log.New(mainGoCoverCheck.logout, "[gocovercheck]", 0)
	wd, err := os.Getwd()
	if err != nil {
		return wraperr(err, "unable to get cwd")
	}
	g.bestGuessPackageName = filepath.Clean(wd)

	if len(g.cmdArgs) > 1 {
		return errors.New("Please only pass one directory to run tests inside\n")
	}
	cmdDir := ""
	if len(g.cmdArgs) == 1 {
		cmdDir = flag.Args()[0]
		g.bestGuessPackageName = cmdDir
	}

	if g.dirout != "" {
		bestGuessFilename := sanitizeForDirectory(g.bestGuessPackageName)
		g.coverprofile = filepath.Join(g.dirout, fmt.Sprintf("%s.code_coverage.txt", bestGuessFilename))
		g.stderr = filepath.Join(g.dirout, fmt.Sprintf("%s.stderr.txt", bestGuessFilename))
		g.stdout = filepath.Join(g.dirout, fmt.Sprintf("%s.stdout.txt", bestGuessFilename))
	}

	cmd := "go"
	args := []string{"test", "-covermode", "atomic"}
	if g.coverprofile == "" {
		if err := g.setupTempCoverProfile(); err != nil {
			return wraperr(err, "unable to create temp cover profile")
		}
	}

	args = append(args, g.setupBasicArgs()...)

	params := make([]string, 0, 5)
	if err := json.Unmarshal([]byte(g.testFlags), &params); err != nil {
		return wraperr(err, "Invalid test flags.  Must be []string{}: %s", g.testFlags)
	}
	args = append(args, params...)

	stdout, err := g.setupRedirect(g.stdout, os.Stdout)
	if err != nil {
		return wraperr(err, "Cannot open stdout pipe file")
	}

	stderr, err := g.setupRedirect(g.stderr, os.Stderr)
	if err != nil {
		return wraperr(err, "Cannot open stderr pipe file")
	}

	e := exec.Command(cmd, args...)
	e.Stdout = stdout
	e.Stderr = stderr
	e.Dir = cmdDir
	g.log.Printf("Running cmd=[%s] args=[%v]\n", cmd, strings.Join(args, " "))
	return g.runCmd(e)
}

func (g *gocovercheck) runCmd(e *exec.Cmd) error {
	runErr := g.cmdRun(e)
	guessedPackageName := guessPackageName(g.log, g.coverprofile)
	if guessedPackageName != defaultPackageName {
		g.bestGuessPackageName = guessedPackageName
	}
	if runErr != nil {
		return wraperr(runErr, "test command did not run correctly")
	}
	g.log.Printf("Finished running command\n")
	coverage, err := calculateCoverage(g.coverprofile)
	if err != nil {
		return wraperr(err, "cannot load coverage profile file")
	}
	g.log.Printf("Calculated coverage %.2f\n", coverage)
	if coverage+.001 < g.requiredCoverage {
		return fmt.Errorf("Code coverage %.4f less than required %.4f", coverage, g.requiredCoverage)
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
