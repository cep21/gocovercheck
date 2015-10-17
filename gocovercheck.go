package main
import (
	"flag"
	"time"
	"strconv"
	"os/exec"
	"io"
	"os"
	"io/ioutil"
	"golang.org/x/tools/cover"
	"fmt"
	"bytes"
	"bufio"
	"strings"
	"path/filepath"
	"log"
)

type gocovercheck struct {
	requiredCoverage float64
	race bool
	timeout time.Duration
	parallel int64
	coverprofile string
	stdout string
	stderr string
	verbose bool

	logout io.Writer
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

func Wrap(err error, fmtString string, args ...interface{}) error {
	return &wrappedError{fmt.Sprintf(fmtString, args...), err}
}

var Main = gocovercheck{
	cmdRun: runCmd,
	logout: ioutil.Discard,
}

func runCmd(c *exec.Cmd) error {
	return c.Run()
}

func init() {
	flag.Float64Var(&Main.requiredCoverage, "required_coverage", 0, "Sets the required coverage for non zero error code.")
	flag.BoolVar(&Main.race, "race", false, "Set race detection")
	flag.DurationVar(&Main.timeout, "timeout", 0, "Timeout testing")
	flag.Int64Var(&Main.parallel, "parallel", 0, "Parallel testing")
	flag.StringVar(&Main.coverprofile, "coverprofile", "", "Coverage profile output")
	flag.StringVar(&Main.stdout, "stdout", "", "File to pipe stdout to.  - means stdout")
	flag.StringVar(&Main.stderr, "stderr", "", "File to pipe stderr to.  - means stderr")

	flag.BoolVar(&Main.verbose, "verbose", false, "If set, will send to stderr verbose logging out")
}

func main() {
	flag.Parse()
	Main.cmdArgs = flag.Args()
	if Main.verbose {
		Main.logout = os.Stderr
	}
	err := Main.main()
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

func (g *gocovercheck) main() error {
	l := log.New(g.logout, "[gocovercheck]", 0)
	cmd := "go"
	args := []string{"test", "-covermode", "atomic"}
	if g.race {
		args = append(args, "-race")
	}
	if g.timeout.Nanoseconds() > 0 {
		args = append(args, "-timeout", g.timeout.String())
	}
	if g.parallel > 0 {
		args = append(args, "-parallel", strconv.FormatInt(g.parallel, 10))
	}
	if g.coverprofile == "" {
		coverProfileFile, err := ioutil.TempFile("", "gocovercheck")
		if err != nil {
			return Wrap(err, "Cannot create gocovercheck temp file")
		}
		defer os.Remove(coverProfileFile.Name())
		coverProfileFile.Close()
		g.coverprofile = coverProfileFile.Name()

		l.Printf("Setting coverprofile to %s\n", g.coverprofile)
	}

	args = append(args, "-coverprofile", g.coverprofile)
	stdout, err := forFile(g.stdout, os.Stdout)
	if err != nil {
		return Wrap(err, "Cannot open stdout pipe file")
	}
	if stdout != os.Stdout {
		defer stdout.Close()
	}
	stderr, err := forFile(g.stderr, os.Stderr)
	if err != nil {
		return Wrap(err, "Cannot open stderr pipe file")
	}
	if stderr != os.Stderr {
		defer stderr.Close()
	}
	args = append(args, g.cmdArgs...)
	e := exec.Command(cmd, args...)
	e.Stdout = stdout
	e.Stderr = stderr
	l.Printf("Running cmd=[%s] args=[%v]\n", cmd, strings.Join(args, ", "))
	if err := g.cmdRun(e); err != nil {
		return Wrap(err, "cannot run command")
	}
	l.Printf("Finished running command\n")
	coverage, err := calculateCoverage(g.coverprofile)
	if err != nil {
		return Wrap(err, "cannot load coverage profile file")
	}
	l.Printf("Calculated coverage %.2f\n", coverage)
	if coverage + .001 < g.requiredCoverage {
		return fmt.Errorf("%s::warning:Code coverage %.3f less than required %f", guessPackageName(g.coverprofile), coverage, g.requiredCoverage)
	}
	return nil
}

var defaultPackageName = ""

func guessPackageName(coverprofile string) string {
	f, err := os.Open(coverprofile)
	if err != nil {
		return defaultPackageName
	}
	defer f.Close()
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
		return 0.0, Wrap(err, "cannot parse coverage profile file %s", coverprofile)
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
