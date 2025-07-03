package main

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"time"

	"github.com/pkg/errors"

	"github.com/logrusorgru/aurora/v4"
	"github.com/rs/zerolog/log"

	"github.com/docker/docker/api/types/mount"
)

type JudgeResult struct {
	Success bool

	Score float64

	Msg string

	Memory uint64 // in bytes
	Time   uint64 // in ns

}

type WorkflowResult struct {
	Success  bool
	Logs     string
	ExitCode int

	Steps []WorkflowStepResult
}

type WorkflowStepResult struct {
	Logs     string
	ExitCode int
}

type Userface struct {
	*bytes.Buffer
	io.Writer
}

func (f Userface) Println(a ...interface{}) (n int, err error) {
	return fmt.Fprintln(f, a...)
}
func (f Userface) Printf(format string, a ...interface{}) (n int, err error) {
	return fmt.Fprintf(f, format, a...)
}

func (f Userface) Write(p []byte) (n int, err error) {
	var _f io.Writer
	if f.Writer != nil {
		_f = io.MultiWriter(f.Buffer, f.Writer)
	} else {
		_f = f.Buffer
	}
	_f.Write(p)
	return len(p), nil
}

type SubmitHash struct {
	Path string
	Hash string
}
type SubmitCtx struct {
	ID      string `gorm:"primaryKey"`
	User    string
	Problem string

	problem *Problem

	SubmitTime int64
	LastUpdate int64

	Status string
	Msg    string

	SubmitDir       string
	SubmitsHashes   SubmitsHashes
	Workdir         string
	WorkflowResults WorkflowResults
	JudgeResult     JudgeResult

	RealWorkdir string

	running  chan struct{}
	Userface Userface
}

func (ctx *SubmitCtx) Update() {
	ctx.LastUpdate = time.Now().UnixNano()
	db.Save(ctx)
}

func (ctx *SubmitCtx) SetStatus(status string) *SubmitCtx {
	ctx.Status = status
	return ctx
}

func (ctx *SubmitCtx) SetMsg(msg string) *SubmitCtx {
	ctx.Msg = msg
	return ctx
}

func GetTime(time.Time) aurora.Value {
	return aurora.Gray(15, time.Now().Format("2006-01-02 15:04:05.000"))
}

func ColorizeScore(res JudgeResult) aurora.Value {
	if !res.Success {
		return aurora.Gray(15, res.Score)
	}
	if res.Score >= 95 {
		return aurora.Green(res.Score)
	} else if res.Score >= 60 {
		return aurora.Yellow(res.Score)
	} else {
		return aurora.Red(res.Score)
	}
}

func ColorizeStatus(status string) aurora.Value {
	switch status {
	case "init":
		return aurora.Gray(10, status)
	case "prep_dirs":
		return aurora.Yellow(status)
	case "prep_files":
		return aurora.Yellow(status)
	case "run_workflow":
		return aurora.Yellow(status)
	case "collect_result":
		return aurora.Yellow(status)
	case "completed":
		return aurora.Green(status)
	case "failed":
		return aurora.Red(status)
	case "dead":
		return aurora.Gray(15, status)
	default:
		return aurora.Bold(status)
	}
}

func RunJudge(ctx *SubmitCtx) {
	log.Debug().Timestamp().Str("id", ctx.ID).Str("user", ctx.User).Str("problem", ctx.Problem).Msg("run judge")

	var start_time = time.Now()

	var err error

	defer func() {
		log.Debug().Timestamp().Str("id", ctx.ID).Str("status", ctx.Status).Str("judgemsg", ctx.Msg).AnErr("err", err).Msg("judge finished")
		ctx.Userface.Println(GetTime(start_time), "Submission", ColorizeStatus(ctx.Status))
		close(ctx.running)

		ctx.Update()
	}()

	ctx.Userface.Println("Submission ID:", aurora.Magenta(ctx.ID))

	ctx.SetStatus("prep_dirs").Update()

	var submits_dir = path.Join(ctx.Workdir, "submits")
	var workflow_dir = path.Join(ctx.Workdir, "work")

	var rsubmits_dir = path.Join(ctx.RealWorkdir, "submits")
	var rworkflow_dir = path.Join(ctx.RealWorkdir, "work")

	err = os.Mkdir(ctx.Workdir, 0700)
	if err != nil {
		goto workdir_creation_failed
	}
	err = os.Mkdir(submits_dir, 0700)
	if err != nil {
		goto workdir_creation_failed
	}
	err = os.Mkdir(workflow_dir, 0700)
	if err != nil {
		goto workdir_creation_failed
	}
	err = os.Chown(ctx.Workdir, cfg.SubmitUid, cfg.SubmitGid)
	if err != nil {
		goto workdir_creation_failed
	}
	err = os.Chown(submits_dir, cfg.SubmitUid, cfg.SubmitGid)
	if err != nil {
		goto workdir_creation_failed
	}
	err = os.Chown(workflow_dir, cfg.SubmitUid, cfg.SubmitGid)
	if err != nil {
		goto workdir_creation_failed
	}

	goto workdir_created

workdir_creation_failed:

	ctx.SetStatus("failed").SetMsg("failed to create submit workdir").Update()
	return

workdir_created:

	log.Debug().Timestamp().Str("id", ctx.ID).Str("submit_workdir", ctx.Workdir).Msg("created working dirs")

	ctx.Userface.Println(GetTime(start_time), "Submitting files")

	ctx.SetStatus("prep_files").Update()

	for _, submit := range ctx.problem.Submits {
		if !submit.IsDir {
			err = SubmitFile(ctx, submits_dir, submit.Path)
			if err != nil {
				ctx.SetStatus("failed").SetMsg("failed to copy submit file " + strconv.Quote(submit.Path)).Update()
				ctx.Userface.Println("	*", aurora.Yellow(submit.Path), ":", aurora.Red("failed"))
				return
			}
		} else {
			// ctx.SubmitDir: eg: /path/to/soj/submits/user
			// submit.Path:   eg: src
			dir_path := ctx.SubmitDir + "/" + submit.Path
			err = filepath.WalkDir(dir_path, func(path string, info fs.DirEntry, err error) error {
				if err != nil {
					return errors.Wrap(err, "failed to execute filepath.WalkDir")
				}
				if !info.IsDir() {
					if filepath.IsAbs(path) { // If path returns relative path
						path, _ = filepath.Rel(dir_path, path)
					}
					// path: eg: subfolder/main.cpp
					// final file path: src/subfolder/main.cpp
					return SubmitFile(ctx, submits_dir, submit.Path+"/"+path) // Concatenate with submit.Path
				}
				return nil
			})
			if err != nil {
				ctx.SetStatus("failed").SetMsg("failed to copy submit directory " + strconv.Quote(submit.Path)).Update()
				ctx.Userface.Println("	*", aurora.Yellow(submit.Path), ":", aurora.Red("failed"))
				return
			}
		}
	}

	log.Debug().Timestamp().Str("id", ctx.ID).Msg("copied submit files")

	ctx.Userface.Println(GetTime(start_time), "Running Judge workflows")

	ctx.SetStatus("run_workflow").Update()

	for idx, workflow := range ctx.problem.Workflow {

		var _mount = []mount.Mount{
			{
				Type:     mount.TypeBind,
				Source:   submits_dir,
				Target:   "/submits",
				ReadOnly: true,
			},
			{
				Type:   mount.TypeBind,
				Source: workflow_dir,
				Target: "/work",
			},
		}

		var envs = []string{
			"SOJ_SUBMITS_DIR=/submits",
			"SOJ_WORK_DIR=/work",
			"SOJ_REAL_WORKDIR=" + rworkflow_dir,
			"SOJ_REAL_SUBMITDIR=" + rsubmits_dir,
			// "SOJ_USER=" + ctx.User,
			"SOJ_PROBLEM=" + ctx.Problem,
			"SOJ_SUBMIT=" + ctx.ID,
			"SOJ_WORK_UID=" + strconv.Itoa(cfg.SubmitUid),
			"SOJ_WORK_GID=" + strconv.Itoa(cfg.SubmitGid),
		}

		for _, mnt := range workflow.Mounts {
			_mount = append(_mount, mount.Mount{
				Type:     mount.Type(mnt.Type),
				Source:   mnt.Source,
				Target:   mnt.Target,
				ReadOnly: mnt.ReadOnly,
			})
		}

		ctx.SetStatus("run_workflow-" + strconv.Itoa(idx)).Update()
		ctx.Userface.Println(GetTime(start_time), "running", "workflow", strconv.Itoa(idx+1), "/", len(ctx.problem.Workflow))

		stepshows := map[int]struct{}{}
		stepprivillege := map[int]struct{}{}

		for _, step := range workflow.Show {
			stepshows[step] = struct{}{}
		}
		for _, step := range workflow.PrivilegedSteps {
			stepprivillege[step] = struct{}{}
		}

		var usr = strconv.Itoa(cfg.SubmitUid)

		if workflow.Root {
			usr = "0"
		}

		ok, cid := RunImage("soj-judge-"+ctx.ID+"-"+strconv.Itoa(idx+1), usr, "soj-judgement", workflow.Image, "/work", _mount, false, false, workflow.DisableNetwork, workflow.Timeout, workflow.NetworkHostMode, envs)

		if !ok {
			ctx.SetStatus("failed").SetMsg("failed to run judge container").Update()
			return
		}

		defer CleanContainer(cid)

		steps := make([]WorkflowStepResult, len(workflow.Steps))

		for sidx, step := range workflow.Steps {
			ctx.SetStatus("run_workflow-" + strconv.Itoa(idx) + "_" + strconv.Itoa(sidx)).Update()

			ctx.Userface.Println(GetTime(start_time), "running", "workflow", strconv.Itoa(idx+1), "step", strconv.Itoa(sidx+1), "/", len(workflow.Steps))

			_, ok := stepshows[sidx+1]
			_, priv := stepprivillege[sidx+1]

			var rr io.Writer = nil
			var re io.Writer = nil
			if ok {
				ctx.Userface.Println("	$", aurora.Yellow(step))
				rr = ColoredIO{ctx.Userface, aurora.BlueFg}
				re = ColoredIO{ctx.Userface, aurora.RedFg}

			}
			ec, logs, err := ExecContainer(cid, step, workflow.Timeout, rr, re, envs, priv)

			if ok {
				ctx.Userface.Println(aurora.Gray(15, "exit code:"), aurora.Yellow(ec))
			}

			if ec != 0 || err != nil {
				ctx.SetStatus("failed").SetMsg("failed to run judge " + strconv.Itoa(idx+1) + " step " + strconv.Itoa(sidx+1)).Update()

				log.Info().Timestamp().Str("id", ctx.ID).Str("image", workflow.Image).Str("step", step).Int("timeout", workflow.Timeout).AnErr("err", err).Str("logs", logs).Int("exitcode", ec).Msg("failed to run judge step")
				return
			}

			steps[sidx] = WorkflowStepResult{
				Logs:     logs,
				ExitCode: ec,
			}

			ctx.Update()
			log.Debug().Timestamp().Str("id", ctx.ID).Str("image", workflow.Image).Str("step", step).Int("timeout", workflow.Timeout).Str("logs", logs).Int("exitcode", ec).Msg("ran judge step")
		}

		logs, err := GetContainerLogs(cid)
		if err != nil {
			ctx.SetStatus("failed").SetMsg("failed to get judge logs").Update()
			return
		}

		ctx.WorkflowResults = append(ctx.WorkflowResults, WorkflowResult{
			Success: true,
			Logs:    logs,
			Steps:   steps,
		})

		log.Debug().Timestamp().Any("mnt", _mount).Str("id", ctx.ID).Str("image", workflow.Image).Str("logs", logs).Msg("got judge logs")

	}

	ctx.SetStatus("collect_result").Update()

	var result_file = workflow_dir + "/result.json"

	_result, err := os.ReadFile(result_file)

	if err != nil {
		log.Info().Timestamp().Str("id", ctx.ID).Str("result_file", result_file).AnErr("err", err).Msg("failed to read result file")
		ctx.SetStatus("failed").SetMsg("failed to read result file").Update()
		return
	}

	err = json.Unmarshal(_result, &ctx.JudgeResult)
	if err != nil {
		log.Info().Timestamp().Str("id", ctx.ID).Str("result_file", result_file).AnErr("err", err).Msg("failed to parse result file")
		ctx.SetStatus("failed").SetMsg("failed to parse result file").Update()
		return
	}

	ctx.SetStatus("completed").SetMsg("judge successfully finished").Update()
}

// CopyFile copies a single file from src to dst and returns the MD5 hash of the copied file.
func CopyFile(src, dst string) (string, error) {
	sourceFile, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer sourceFile.Close()

	destinationFile, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer destinationFile.Close()

	hash := md5.New()
	if _, err = io.Copy(destinationFile, io.TeeReader(sourceFile, hash)); err != nil {
		return "", err
	}

	if err := destinationFile.Sync(); err != nil {
		return "", err
	}

	// Calculate the MD5 sum of the file that has been copied.
	md5String := hex.EncodeToString(hash.Sum(nil))
	return md5String, nil
}

// SubmitFile adds a file to the problem's submition list.
func SubmitFile(ctx *SubmitCtx, submits_dir string, submit_path string) error {
	var src_submit_path = path.Join(ctx.SubmitDir, submit_path)
	var dst_submit_path = path.Join(submits_dir, submit_path)

	os.MkdirAll(path.Dir(dst_submit_path), 0700)
	os.Chown(path.Dir(dst_submit_path), cfg.SubmitUid, cfg.SubmitGid)

	hash, err := CopyFile(src_submit_path, dst_submit_path)
	if err != nil {
		return err
	} else {
		os.Chown(dst_submit_path, cfg.SubmitUid, cfg.SubmitGid)
		os.Chmod(dst_submit_path, 0400)

		log.Debug().Timestamp().Str("id", ctx.ID).Str("submit_file", submit_path).Str("hash", hash).Msg("copied submit file")
		// ctx.SubmitsHashes[submit.Path] = hash

		ctx.SubmitsHashes = append(ctx.SubmitsHashes, SubmitHash{
			Hash: hash,
			Path: submit_path,
		})

		ctx.Userface.Println("	*", aurora.Yellow(submit_path), ":", aurora.Blue(hash))
	}

	return nil
}

type ColoredIO struct {
	io.Writer
	aurora.Color
}

func (c ColoredIO) Write(p []byte) (n int, err error) {
	_, err = c.Writer.Write([]byte(aurora.Colorize(string(p), c.Color).String()))
	return len(p), err
}
