package main
import (
	"testing"
	. "github.com/smartystreets/goconvey/convey"
	"os"
	"os/exec"
	"io/ioutil"
)

func TestForFile(t *testing.T) {
	Convey("When loading a file", t, func() {
		Convey("- should return identity", func() {
			r, err := forFile("-", os.Stdout)
			So(r, ShouldEqual, os.Stdout)
			So(err, ShouldBeNil)
		})
		Convey("empty should return useless item", func() {
			r, err := forFile("-", os.Stdout)
			So(err, ShouldBeNil)
			_, err = r.Write(nil)
			So(err, ShouldBeNil)
		})
	})
}

func TestForMyself(t *testing.T) {
	Convey("My own coverage", t, func() {
		g := gocovercheck{
			stdout: "",
			stderr: "",
			cmdRun: func(*exec.Cmd) error {
				return nil
			},
			logout: ioutil.Discard,
		}
		Convey("should run", func() {
			err := g.main()
			So(err, ShouldBeNil)
		})
		Convey("should not be more than zero", func() {
			g.requiredCoverage = 1
			err := g.main()
			t.Log(err.Error())
			So(err, ShouldNotBeNil)
		})
	})
}
