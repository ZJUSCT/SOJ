package main

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"github.com/logrusorgru/aurora/v4"
	"github.com/rs/zerolog/log"

	"github.com/docker/docker/client"
	ssh "github.com/gliderlabs/ssh"
	"github.com/rs/zerolog"
	gossh "golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"
)

type Config struct {
	HostKey    string `yaml:"HostKey"`
	ListenAddr string `yaml:"ListenAddr"`
	APIAddr    string `yaml:"APIAddr"`

	AllowedSSHPubkey string `yaml:"AllowedSSHPubkey"`

	SubmitsDir    string `yaml:"SubmitsDir"`
	SubmitWorkDir string `yaml:"SubmitWorkDir"`
	ProblemsDir   string `yaml:"ProblemsDir"`

	RealSubmitsDir    string `yaml:"RealSubmitsDir"`
	RealSubmitWorkDir string `yaml:"RealSubmitWorkDir"`

	SqlitePath string `yaml:"SqlitePath"`

	DockerCli        string `yaml:"DockerCli"`
	ProblemURLPrefix string `yaml:"ProblemURLPrefix"`

	SubmitGid int `yaml:"SubmitGid"`
	SubmitUid int `yaml:"SubmitUid"`

	Admins []string `yaml:"Admins"`
}

var cfg = Config{}

var paused = false

func main() {

	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	_cfg, err := os.ReadFile("config.yaml")
	if err != nil {
		log.Fatal().Err(err).Msg("failed to read config file")
	}

	err = yaml.Unmarshal(_cfg, &cfg)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to parse config file")
	}

	var pubkey gossh.PublicKey
	if cfg.AllowedSSHPubkey != "" {
		pubkey, _, _, _, err = gossh.ParseAuthorizedKey([]byte(cfg.AllowedSSHPubkey))
		if err != nil {
			log.Fatal().Err(err).Msg("failed to parse allowed ssh pubkey")
		}
	} else {
		log.Warn().Msg("no allowed ssh pubkey specified, allowing all")
	}

	docker_cli, err = client.NewClientWithOpts(client.FromEnv)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create docker client")
	}

	pk, err := gossh.ParsePrivateKey([]byte(cfg.HostKey))
	if err != nil {
		log.Fatal().Err(err).Msg("failed to parse host key")
	}

	db, err = gorm.Open(sqlite.Open(cfg.SqlitePath), &gorm.Config{})
	if err != nil {
		log.Fatal().Err(err).Msg("failed to open sqlite")
	}

	db.AutoMigrate(&SubmitCtx{})
	db.AutoMigrate(&User{})

	db.Model(&SubmitCtx{}).Where("status != ? AND status != ? AND status != ?", "completed", "dead", "failed").Update("status", "dead")

	problems := LoadProblemDir(cfg.ProblemsDir)

	DoFULLUserScan(problems)

	serveHTTP(cfg.APIAddr)

	s := &ssh.Server{
		Addr: cfg.ListenAddr,
		Handler: func(s ssh.Session) {
			uf := Userface{
				Buffer: bytes.NewBuffer(nil),
				Writer: s,
			}

			cmds := s.Command()
			log.Info().Str("user", s.User()).Strs("cmds", cmds).Msg("new session")

			if len(cmds) == 0 {
				uf.Println("Welcome to", aurora.Bold("SOJ"), aurora.Gray(aurora.GrayIndex(10), "Secure Online Judge"), ",", aurora.BrightBlue(s.User()))
				uf.Println(aurora.Yellow(time.Now().Format(time.DateTime + " MST")))
				uf.Println("Use 'submit", aurora.Gray(15, "(sub)"), "<problem_id>' to submit a problem")
				uf.Println("Use 'list", aurora.Gray(15, "(ls)"), "[page]' to list your submissions")
				uf.Println("Use 'status", aurora.Gray(15, "(st)"), "<submit_id>' to show a submission", aurora.Magenta("(fuzzy match)"))
				uf.Println("Use 'rank", aurora.Gray(15, "(rk)"), "' to show ranklist")
				uf.Println("Use 'my' to show your submission summary")
				// uf.Println("Use 'problems' to list problems")
				uf.Println()

			} else {
				uf.Println(aurora.Yellow(time.Now().Format(time.DateTime + " MST")))

				switch cmds[0] {
				// case "problems":
				// 	uf.Println("Problems:", aurora.Italic(aurora.Gray(15, "(click to show in browser)")))
				// 	for k := range problems {
				// 		url := cfg.ProblemURLPrefix + k
				// 		uf.Println("	", aurora.Bold(k), aurora.Hyperlink(url, url))
				// 	}
				case "rank", "rk":
					usrs := make([]User, 0)

					var prblmss []string
					for k := range problems {
						prblmss = append(prblmss, k)
					}

					sort.Strings(prblmss)

					db.Order("total_score desc").Find(&usrs)

					var ranks []string

					var cursoc float64 = -1
					var currk int = 0
					for i := range usrs {
						if usrs[i].TotalScore != cursoc {
							currk = i
							cursoc = usrs[i].TotalScore
						}
						ranks = append(ranks, strconv.Itoa(currk+1))

					}

					var userss []string
					for _, u := range usrs {
						userss = append(userss, u.ID)
					}

					var totalscores []string
					for _, u := range usrs {
						totalscores = append(totalscores, fmt.Sprintf("%.2f", u.TotalScore))
					}

					var bestscores [][]string

					for _, p := range prblmss {
						var scores []string
						for _, u := range usrs {
							scores = append(scores, fmt.Sprintf("%.2f", u.BestScores[p]))
						}
						bestscores = append(bestscores, scores)
					}

					var colc = make([]aurora.Color, len(prblmss))
					for i := range colc {
						colc[i] = aurora.WhiteFg | aurora.UnderlineFm
					}

					MkTable(uf, append([]string{"Rank", "User", "Total"}, prblmss...), append([]aurora.Color{aurora.BoldFm | aurora.YellowFg, aurora.BoldFm | aurora.WhiteFg, aurora.BoldFm | aurora.GreenFg}, colc...), append([][]string{ranks, userss, totalscores}, bestscores...))

				case "submit", "sub":
					if len(cmds) != 2 {
						uf.Println(aurora.Red("error:"), "invalid arguments")
						uf.Println("usage: submit <problem_id>")
						return
					}
					if paused {
						uf.Println(aurora.Red("error:"), "submit is paused. Please try again later")
						return
					}

					pid := cmds[1]

					pb, ok := problems[pid]
					if !ok {
						uf.Println(aurora.Red("error:"), "problem", aurora.Yellow(strconv.Quote(pid)), "not found")
						return
					}

					uf.Println(aurora.Green("Submitting"), aurora.Bold(pid))
					subtime := time.Now()

					id := strconv.Itoa(int(subtime.UnixNano()))
					ctx := SubmitCtx{
						ID:      id,
						Problem: pid,
						problem: &pb,
						User:    s.User(),

						SubmitTime: subtime.UnixNano(),

						Status: "init",

						SubmitDir: path.Join(cfg.SubmitsDir, s.User(), pid),
						Workdir:   path.Join(cfg.SubmitWorkDir, id),

						RealWorkdir: path.Join(cfg.RealSubmitWorkDir, id),

						Userface: Userface{
							Buffer: bytes.NewBuffer(nil),
							Writer: uf,
						},
						// JudgeResult: JudgeResult{Score: -1},
						running: make(chan struct{}),
					}

					go RunJudge(&ctx)

					<-ctx.running

					uf.Println("Submit", "is", ColorizeStatus(ctx.Status))
					uf.Println("Message:\n	", aurora.Blue(ctx.Msg))

					WriteResult(uf, ctx)

					UserUpdate(s.User(), ctx)

				case "list", "ls":
					if len(cmds) > 2 {
						uf.Println(aurora.Red("error:"), "invalid arguments")
						uf.Println("usage: list [page]")
						return
					}

					uf.Println(aurora.Green("Listing"), aurora.Bold("submissions"))

					page := 1

					if len(cmds) == 2 {
						var err error
						page, err = strconv.Atoi(cmds[1])
						if err != nil {
							uf.Println(aurora.Red("error:"), "invalid page number")
							return
						}
					}

					var submits []SubmitCtx
					//paging
					// reverse order

					// db.Where("user = ?", s.User()).Offset((page - 1) * 10).Limit(10).Find(&submits)
					db.Where("user = ?", s.User()).Order("submit_time desc").Offset((page - 1) * 10).Limit(10).Find(&submits)

					var total int64
					db.Model(&SubmitCtx{}).Where("user = ?", s.User()).Count(&total)

					uf.Println(aurora.Cyan("Page"), aurora.Bold(page), "of", aurora.Yellow(total/10+1))

					ListSubs(uf, submits)

				case "status", "st":
					if len(cmds) != 2 {
						uf.Println(aurora.Red("error:"), "invalid arguments")
						uf.Println("usage: status <submit_id>")
						return
					}

					uf.Println(aurora.Green("Showing"), aurora.Bold("submission"), aurora.Magenta(cmds[1]))

					var submit SubmitCtx
					tx := db.Order("submit_time desc").Where("id LIKE ? AND user = ?", "%"+cmds[1]+"%", s.User()).First(&submit)
					if tx.Error != nil {
						uf.Println(aurora.Red("error:"), "submit", aurora.Yellow(strconv.Quote(cmds[1])), "not found")
						return
					}

					uf.Println()

					ShowSub(uf, submit, problems)

				case "my":
					uf.Println("User", aurora.Bold(aurora.BrightWhite(s.User())))

					var user User

					db.Where("ID = ?", s.User()).Find(&user)

					if user.ID == "" {
						uf.Println(aurora.Gray(15, "No submissions yet"))
						return
					}

					var prblmss []string
					for k := range problems {
						prblmss = append(prblmss, k)
					}

					sort.Strings(prblmss)

					Cols := []string{"Problem", "Score", "Weight", "Submit ID", "Date"}
					var ColLongest = make([]int, len(Cols))
					for i, col := range Cols {
						ColLongest[i] = len(col)
					}

					var map_succ map[string]bool = make(map[string]bool)

					for _, problem_id := range prblmss {
						sco, ok := user.BestScores[problem_id]
						if ok {
							map_succ[problem_id] = true
						}
						ColLongest[0] = max(ColLongest[0], len(problem_id))
						ColLongest[1] = max(ColLongest[1], len(fmt.Sprintf("%.2f", sco/problems[problem_id].Weight)))
						ColLongest[2] = max(ColLongest[2], len(fmt.Sprintf("%.2f", problems[problem_id].Weight)))
						ColLongest[3] = max(ColLongest[3], len(user.BestSubmits[problem_id]))
						ColLongest[4] = max(ColLongest[4], len(time.Unix(0, user.BestSubmitDate[problem_id]).Format(time.DateTime+" MST")))
					}

					for i, col := range Cols {
						uf.Printf("%-*s ", ColLongest[i], col)
					}

					uf.Println()
					for _, problem_id := range prblmss {
						uf.Printf("%-*s %-*.2f %-*.2f %-*s %-*s\n",
							ColLongest[0], aurora.Bold(aurora.Italic(problem_id)),
							ColLongest[1], aurora.Bold(ColorizeScore(JudgeResult{Success: map_succ[problem_id], Score: user.BestScores[problem_id] / problems[problem_id].Weight})),
							ColLongest[2], aurora.Bold(problems[problem_id].Weight),
							ColLongest[3], aurora.Magenta(user.BestSubmits[problem_id]),
							ColLongest[4],
							func() aurora.Value {
								if map_succ[problem_id] {
									return aurora.Yellow(time.Unix(0, user.BestSubmitDate[problem_id]).Format(time.DateTime + " MST"))
								} else {
									return aurora.Gray(15, "N/A")
								}
							}())
					}

					uf.Println()
					uf.Println("Total Score:", aurora.Bold(aurora.BrightWhite(user.TotalScore)))

				case "adm":
					if !IsAdmin(s.User()) {
						s.Write([]byte("unknown command " + strconv.Quote(s.RawCommand()) + "\n"))
						return
					}

					if len(cmds) < 2 {
						uf.Println(aurora.Red("error:"), "invalid arguments")
						uf.Println("usage: adm <command>")
						return
					}
					switch cmds[1] {
					case "list":
						page := 1
						if len(cmds) == 3 {
							var err error
							page, err = strconv.Atoi(cmds[2])
							if err != nil {
								uf.Println(aurora.Red("error:"), "invalid page number")
								return
							}
						}

						var submits []SubmitCtx
						//paging
						// reverse order

						// db.Where("user = ?", s.User()).Offset((page - 1) * 10).Limit(10).Find(&submits)
						db.Order("submit_time desc").Offset((page - 1) * 20).Limit(20).Find(&submits)

						var total int64
						db.Model(&SubmitCtx{}).Count(&total)

						uf.Println(aurora.Cyan("Page"), aurora.Bold(page), "of", aurora.Yellow(total/20+1))

						ListSubs(uf, submits)
					case "status":
						if len(cmds) != 3 {
							uf.Println(aurora.Red("error:"), "invalid arguments")
							uf.Println("usage: adm status <submit_id>")
							return
						}

						uf.Println(aurora.Green("Showing"), aurora.Bold("submission"), aurora.Magenta(cmds[2]))

						var submit SubmitCtx
						tx := db.Where("id = ?", cmds[2]).First(&submit)
						if tx.Error != nil {
							uf.Println(aurora.Red("error:"), "submit", aurora.Yellow(strconv.Quote(cmds[2])), "not found")
							return
						}

						uf.Println()

						ShowSub(uf, submit, problems)
					case "pause":
						paused = true
						uf.Println(aurora.Green("Submit"), aurora.Bold("paused"))
					case "reload":
						problems = LoadProblemDir(cfg.ProblemsDir)
						uf.Println(aurora.Green("Problems"), aurora.Bold("reloaded"))
					}

				default:
					s.Write([]byte("unknown command " + strconv.Quote(s.RawCommand()) + "\n"))
				}
			}

		},
		SubsystemHandlers: map[string]ssh.SubsystemHandler{
			"sftp": SftpHandler,
		},
		PublicKeyHandler: func(ctx ssh.Context, key ssh.PublicKey) bool {
			return pubkey == nil || ssh.KeysEqual(pubkey, key)
		},
	}
	s.AddHostKey(pk)

	log.Info().Str("addr", cfg.ListenAddr).Msg("listening")
	err = s.ListenAndServe()
	if err != nil {
		log.Fatal().Err(err).Msg("failed to listen")
	}
}

func WriteResult(uf Userface, res SubmitCtx) {
	if res.Status != "completed" {
		uf.Println(aurora.Italic(aurora.Underline(aurora.Bold(aurora.Gray(15, "No judgement result")))))
		uf.Println()
		return
	}
	if res.JudgeResult.Success {
		uf.Printf("Score %.2f %s\n", aurora.Underline(aurora.Bold(ColorizeScore(res.JudgeResult))), aurora.Italic(aurora.Gray(15, "max.100 (Unweighted)")))
	} else {
		uf.Println(aurora.Red("Judgement is Failed"))
	}

	uf.Println("Judgement Message:")

	if len(res.JudgeResult.Msg) > 0 {
		uf.Println(aurora.Bold(aurora.Cyan("	" + strings.ReplaceAll(res.JudgeResult.Msg, "\n", "\n	"))))
	} else {
		uf.Println("	", aurora.Gray(15, "No message"))
	}
	uf.Println()
}

func ListSubs(uf Userface, submits []SubmitCtx) {

	if len(submits) == 0 {
		uf.Println(aurora.Gray(15, "No submissions yet"))
	} else {
		// t.AddHeader("ID", "Problem", "Status", "Message", "Score", "Judge Message")
		Cols := []string{"ID", "User", "Problem", "Status", "Message", "Score", "Judge Message", "Date"}
		var ColLongest = make([]int, len(Cols))
		for i, col := range Cols {
			ColLongest[i] = len(col)
		}

		for _, submit := range submits {
			ColLongest[0] = max(ColLongest[0], len(submit.ID))
			ColLongest[1] = max(ColLongest[1], len(submit.User))
			ColLongest[2] = max(ColLongest[2], len(submit.Problem))
			ColLongest[3] = max(ColLongest[3], len(submit.Status))
			ColLongest[4] = max(ColLongest[4], len(submit.Msg))
			ColLongest[5] = max(ColLongest[5], len(fmt.Sprintf("%.2f", submit.JudgeResult.Score)))
			ColLongest[6] = max(ColLongest[6], len(OmitStr(submit.JudgeResult.Msg, 20)))
			ColLongest[7] = max(ColLongest[7], len(time.Unix(0, submit.SubmitTime).Format(time.DateTime)))
		}

		for i, col := range Cols {
			uf.Printf("%-*s ", ColLongest[i], col)
		}
		uf.Println()
		for _, submit := range submits {
			// uf.Println(aurora.Magenta(submit.ID), submit.Problem, ColorizeStatus(submit.Status), aurora.Blue(submit.Msg), aurora.Bold(submit.JudgeResult.Score))
			uf.Printf("%-*s %-*s %-*s %-*s %-*s %-*.2f %-*s %-*s\n",
				ColLongest[0], aurora.Magenta(submit.ID),
				ColLongest[1], aurora.Bold(aurora.BrightWhite(submit.User)),
				ColLongest[2], aurora.Bold(aurora.Italic(submit.Problem)),
				ColLongest[3], ColorizeStatus(submit.Status),
				ColLongest[4], aurora.Blue(submit.Msg),
				ColLongest[5], aurora.Bold(ColorizeScore(submit.JudgeResult)),
				ColLongest[6], aurora.Bold(aurora.Blue(OmitStr(submit.JudgeResult.Msg, 20))),
				ColLongest[7], aurora.Yellow(time.Unix(0, submit.SubmitTime).Format(time.DateTime+" MST")))
		}
	}
}

func ShowSub(uf Userface, submit SubmitCtx, problems map[string]Problem) {
	uf.Println("Submit", "Status:", ColorizeStatus(submit.Status))
	uf.Println("Submit ID:", aurora.Magenta(submit.ID))
	uf.Println("Submit Time:", aurora.Yellow(time.Unix(0, submit.SubmitTime).Format(time.DateTime+" MST")))
	uf.Println("Last Update:", aurora.Yellow(time.Unix(0, submit.LastUpdate).Format(time.DateTime+" MST")))
	uf.Println("User", aurora.Bold(aurora.BrightWhite(submit.User)))

	if len(submit.Msg) > 0 {
		uf.Println("Message:\n	", aurora.Blue(submit.Msg))
	} else {
		uf.Println("Message:\n	", aurora.Gray(15, "No message"))
	}

	uf.Println()

	if prob, ok := problems[submit.Problem]; ok {
		uf.Println("Problem:", aurora.Bold(submit.Problem))
		uf.Println("Problem Weight:", aurora.Bold(prob.Weight))
	} else {
		uf.Println("Problem:", aurora.Bold(submit.Problem), aurora.Gray(15, "(not found)"))
	}

	uf.Println()

	uf.Println("Logs:")
	uf.Write(submit.Userface.Buffer.Bytes())

	uf.Println()

	WriteResult(uf, submit)

	// c, _ := json.Marshal(submit)
	// fmt.Println(string(c))
}

func MkTable(uf Userface, cols []string, colc []aurora.Color, data [][]string) {
	var ColLongest = make([]int, len(cols))
	for i, col := range cols {
		ColLongest[i] = len(col)
	}

	for i := 0; i < len(data[0]); i++ {
		for j := 0; j < len(cols); j++ {
			ColLongest[j] = max(ColLongest[j], len(data[j][i]))
		}
	}

	for i, col := range cols {
		uf.Printf("%-*s ", ColLongest[i], col)
	}
	uf.Println()

	// for _, row := range data {
	// 	for i, col := range row {
	// 		uf.Printf("%-*s ", ColLongest[i], col)
	// 	}
	// 	uf.Println()
	// }
	for i := 0; i < len(data[0]); i++ {
		for j := 0; j < len(cols); j++ {
			uf.Printf("%-*s ", ColLongest[j], aurora.Colorize(data[j][i], colc[j]))
		}
		uf.Println()
	}

}

func IsAdmin(user string) bool {
	for _, a := range cfg.Admins {
		if a == user {
			return true
		}
	}
	return false
}

func OmitStr(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}
