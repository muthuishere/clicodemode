package codemode

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/muthuishere/clicodemode/js"
)

// ExecPolicy controls external processes. The zero value denies all of them:
// a host must opt in by naming programs.
type ExecPolicy struct {
	// Programs that may run, by base name ("git") or absolute path.
	// "*" allows any program on PATH (use with care).
	Programs []string
	// Allow, if set, is consulted after Programs for every argv.
	Allow func(ctx context.Context, argv []string) error
	// Shell enables shell("..."), which runs `sh -c`. Independent of Programs:
	// a shell can run anything, so it needs its own explicit authorization.
	Shell bool
	// Env names of environment variables passed through to children, on top
	// of PATH, HOME, LANG, TMPDIR. Values are never shown to the program.
	Env []string
	// Dir is the working directory for children (default: current).
	Dir string
	// MaxOutput caps captured stdout/stderr bytes for exec() (default 8 MiB).
	MaxOutput int
}

func (p ExecPolicy) check(ctx context.Context, argv []string) error {
	if len(argv) == 0 || argv[0] == "" {
		return errors.New("argv must be a non-empty array of strings")
	}
	ok := false
	for _, prog := range p.Programs {
		if prog == "*" || prog == argv[0] {
			ok = true
			break
		}
	}
	if !ok {
		if len(p.Programs) == 0 {
			return fmt.Errorf("running %q is not permitted: this host allows no external programs", argv[0])
		}
		return fmt.Errorf("running %q is not permitted (allowed: %s)", argv[0], strings.Join(p.Programs, ", "))
	}
	if p.Allow != nil {
		return p.Allow(ctx, argv)
	}
	return nil
}

func (p ExecPolicy) env() []string {
	names := append([]string{"PATH", "HOME", "LANG", "TMPDIR", "SYSTEMROOT"}, p.Env...)
	var out []string
	for _, n := range names {
		if v, ok := os.LookupEnv(n); ok {
			out = append(out, n+"="+v)
		}
	}
	return out
}

type execOpts struct {
	stdin   *string
	timeout time.Duration
	cwd     string
}

func parseArgv(v js.Value) ([]string, error) {
	arr, ok := js.ToGo(v).([]any)
	if !ok {
		return nil, errors.New("argv must be an array of strings, e.g. [\"git\", \"status\"]; there is no implicit shell")
	}
	argv := make([]string, len(arr))
	for i, a := range arr {
		s, ok := a.(string)
		if !ok {
			return nil, fmt.Errorf("argv[%d] must be a string", i)
		}
		argv[i] = s
	}
	return argv, nil
}

// resolveCwd confines a requested cwd to base (ExecPolicy.Dir, or the process
// working directory when empty). Relative paths are taken against base; both
// sides are made absolute and symlink-resolved so ../ and links cannot escape.
func resolveCwd(base, cwd string) (string, error) {
	if base == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", fmt.Errorf("opts.cwd: cannot determine working directory: %v", err)
		}
		base = wd
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("opts.cwd: %v", err)
	}
	if r, err := filepath.EvalSymlinks(absBase); err == nil {
		absBase = r
	}
	target := cwd
	if !filepath.IsAbs(target) {
		target = filepath.Join(absBase, target)
	}
	target, err = filepath.Abs(target)
	if err != nil {
		return "", fmt.Errorf("opts.cwd: %v", err)
	}
	if r, err := filepath.EvalSymlinks(target); err == nil {
		target = r
	}
	rel, err := filepath.Rel(absBase, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("opts.cwd %q is outside the allowed directory %s", cwd, absBase)
	}
	return target, nil
}

func parseOpts(v js.Value, baseDir string) (execOpts, error) {
	var o execOpts
	m, _ := js.ToGo(v).(map[string]any)
	for k, val := range m {
		switch k {
		case "stdin":
			s, ok := val.(string)
			if !ok {
				return o, errors.New("opts.stdin must be a string")
			}
			o.stdin = &s
		case "timeoutMs":
			f, ok := val.(float64)
			if !ok || f <= 0 {
				return o, errors.New("opts.timeoutMs must be a positive number")
			}
			o.timeout = time.Duration(f) * time.Millisecond
		case "cwd":
			s, ok := val.(string)
			if !ok {
				return o, errors.New("opts.cwd must be a string")
			}
			if s != "" {
				dir, err := resolveCwd(baseDir, s)
				if err != nil {
					return o, err
				}
				o.cwd = dir
			}
		default:
			return o, fmt.Errorf("unknown exec option %q (stdin, timeoutMs, cwd)", k)
		}
	}
	return o, nil
}

// capped is an io.Writer that keeps at most n bytes.
type capped struct {
	buf       bytes.Buffer
	n         int
	truncated bool
}

func (c *capped) Write(p []byte) (int, error) {
	room := c.n - c.buf.Len()
	if room <= 0 {
		c.truncated = true
		return len(p), nil
	}
	if len(p) > room {
		c.buf.Write(p[:room])
		c.truncated = true
		return len(p), nil
	}
	return c.buf.Write(p)
}

func (e *Engine) command(ctx context.Context, argv []string, o execOpts) *exec.Cmd {
	pol := e.Policy.Exec
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = pol.env()
	cmd.Dir = pol.Dir
	if o.cwd != "" {
		cmd.Dir = o.cwd
	}
	if o.stdin != nil {
		cmd.Stdin = strings.NewReader(*o.stdin)
	}
	cmd.WaitDelay = 2 * time.Second // don't hang on children holding pipes
	return cmd
}

// installExec defines exec, execStream and shell.
func (e *Engine) installExec(it *js.Interp) {
	pol := e.Policy.Exec
	runExec := func(r *js.Runner, argv []string, optsV js.Value) (js.Value, error) {
		if err := pol.check(r.Interp().Context(), argv); err != nil {
			return nil, typeErr("exec: " + err.Error())
		}
		o, err := parseOpts(optsV, pol.Dir)
		if err != nil {
			return nil, typeErr("exec: " + err.Error())
		}
		max := pol.MaxOutput
		if max <= 0 {
			max = 8 << 20
		}
		return r.Interp().HostPromise(func(ctx context.Context) (js.Value, error) {
			if o.timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, o.timeout)
				defer cancel()
			}
			cmd := e.command(ctx, argv, o)
			stdout, stderr := &capped{n: max}, &capped{n: max}
			cmd.Stdout, cmd.Stderr = stdout, stderr
			err := cmd.Run()
			code := 0
			var ee *exec.ExitError
			switch {
			case err == nil:
			case errors.As(err, &ee) && ctx.Err() == nil:
				code = ee.ExitCode()
			case ctx.Err() != nil:
				return nil, fmt.Errorf("exec %s: %v", argv[0], ctx.Err())
			default:
				return nil, fmt.Errorf("exec %s: %v", argv[0], err)
			}
			// Same shape as a WFNexus `run` step output.
			res := map[string]any{
				"ok": code == 0, "exitCode": code,
				"stdout": stdout.buf.String(), "stderr": stderr.buf.String(),
			}
			if stdout.truncated || stderr.truncated {
				res["truncated"] = true
			}
			return js.FromGo(res), nil
		}), nil
	}
	it.Define("exec", &js.Builtin{Name: "exec", Fn: func(r *js.Runner, _ js.Value, a []js.Value) (js.Value, error) {
		argv, err := parseArgv(argAt(a, 0))
		if err != nil {
			return nil, typeErr("exec: " + err.Error())
		}
		return runExec(r, argv, argAt(a, 1))
	}})
	it.Define("shell", &js.Builtin{Name: "shell", Fn: func(r *js.Runner, _ js.Value, a []js.Value) (js.Value, error) {
		if !pol.Shell {
			return nil, typeErr("shell: not permitted by this host (shell access needs its own authorization; use exec([...]) with an argv array)")
		}
		cmdline, ok := argAt(a, 0).(string)
		if !ok {
			return nil, typeErr("shell(command): command must be a string")
		}
		o, err := parseOpts(argAt(a, 1), pol.Dir)
		if err != nil {
			return nil, typeErr("shell: " + err.Error())
		}
		max := pol.MaxOutput
		if max <= 0 {
			max = 8 << 20
		}
		// shell bypasses Programs on purpose: Shell is the authorization.
		return r.Interp().HostPromise(func(ctx context.Context) (js.Value, error) {
			cmd := e.command(ctx, []string{"sh", "-c", cmdline}, o)
			stdout, stderr := &capped{n: max}, &capped{n: max}
			cmd.Stdout, cmd.Stderr = stdout, stderr
			err := cmd.Run()
			code := 0
			var ee *exec.ExitError
			if err != nil {
				if errors.As(err, &ee) && ctx.Err() == nil {
					code = ee.ExitCode()
				} else {
					return nil, fmt.Errorf("shell: %v", err)
				}
			}
			return js.FromGo(map[string]any{"ok": code == 0, "exitCode": code, "stdout": stdout.buf.String(), "stderr": stderr.buf.String()}), nil
		}), nil
	}})
	it.Define("execStream", &js.Builtin{Name: "execStream", Fn: func(r *js.Runner, _ js.Value, a []js.Value) (js.Value, error) {
		argv, err := parseArgv(argAt(a, 0))
		if err != nil {
			return nil, typeErr("execStream: " + err.Error())
		}
		if err := pol.check(r.Interp().Context(), argv); err != nil {
			return nil, typeErr("execStream: " + err.Error())
		}
		o, err := parseOpts(argAt(a, 1), pol.Dir)
		if err != nil {
			return nil, typeErr("execStream: " + err.Error())
		}
		return js.NewStream(e.processSource(argv, o)), nil
	}})
}

// processSource streams a process's stdout lines. Cancelling the context
// (early take(), error, run timeout) kills the process and closes its pipes.
// A non-zero exit after a full read fails the stream with the stderr tail.
func (e *Engine) processSource(argv []string, o execOpts) js.Source {
	return func(ctx context.Context) (<-chan js.Value, func() error) {
		cancel := func() {}
		if o.timeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, o.timeout)
		}
		cmd := e.command(ctx, argv, o)
		stderr := &capped{n: 64 << 10}
		cmd.Stderr = stderr
		pr, err := cmd.StdoutPipe()
		if err == nil {
			err = cmd.Start()
		}
		if err != nil {
			cancel()
			ch := make(chan js.Value)
			close(ch)
			return ch, func() error { return fmt.Errorf("execStream %s: %v", argv[0], err) }
		}
		lines, waitLines := js.FromReader(io.NopCloser(pr))(ctx)
		var once sync.Once
		var werr error
		return lines, func() error {
			once.Do(func() {
				defer cancel()
				lerr := waitLines()
				err := cmd.Wait()
				var ee *exec.ExitError
				switch {
				case ctx.Err() == context.DeadlineExceeded:
					werr = fmt.Errorf("execStream %s: timed out after %v", argv[0], o.timeout)
				case ctx.Err() != nil:
				case errors.As(err, &ee):
					tail := strings.TrimSpace(stderr.buf.String())
					if len(tail) > 500 {
						tail = "..." + tail[len(tail)-500:]
					}
					werr = fmt.Errorf("execStream %s: exit %d: %s", argv[0], ee.ExitCode(), tail)
				case err != nil:
					werr = fmt.Errorf("execStream %s: %v", argv[0], err)
				case lerr != nil:
					werr = lerr
				}
			})
			return werr
		}
	}
}

func argAt(a []js.Value, i int) js.Value {
	if i < len(a) {
		return a[i]
	}
	return js.Undefined
}
