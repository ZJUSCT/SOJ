package main

import (
	"database/sql/driver"
	"encoding/json"
)

type User struct {
	ID string `gorm:"primaryKey"`

	BestScores     JMapStrFloat64
	BestSubmits    JMapStrString
	BestSubmitDate JMapStrInt64

	TotalScore float64
}

type JMapStrFloat64 map[string]float64
type JMapStrString map[string]string
type JMapStrInt64 map[string]int64

func (u *User) CalculateTotalScore() {
	var total float64
	for _, s := range u.BestScores {
		total += s
	}
	u.TotalScore = total
}

func DoFULLUserScan(pmbls map[string]Problem) {

	var _submits []SubmitCtx
	db.Find(&_submits)

	users := make(map[string]User)

	for _, s := range _submits {
		u, ok := users[s.User]
		if !ok {
			u = User{
				ID:             s.User,
				BestScores:     make(map[string]float64),
				BestSubmits:    make(map[string]string),
				BestSubmitDate: make(map[string]int64),
			}
		}

		if s.Status == "completed" {
			if u.BestScores[s.Problem] < s.JudgeResult.Score*pmbls[s.Problem].Weight {
				u.BestScores[s.Problem] = s.JudgeResult.Score * pmbls[s.Problem].Weight
				u.BestSubmits[s.Problem] = s.ID
				u.BestSubmitDate[s.Problem] = s.SubmitTime
			}
		}

		users[s.User] = u
	}

	for _, u := range users {
		u.CalculateTotalScore()
		db.Save(&u)
	}

}

func UserUpdate(user string, s SubmitCtx) {
	var u User
	db.First(&u, "id = ?", user)

	if u.ID == "" {
		u.ID = user
		u.BestScores = make(map[string]float64)
		u.BestSubmits = make(map[string]string)
		u.BestSubmitDate = make(map[string]int64)
	}
	if s.Status == "completed" {
		if u.BestScores[s.Problem] < s.JudgeResult.Score*s.problem.Weight {
			u.BestScores[s.Problem] = s.JudgeResult.Score * s.problem.Weight
			u.BestSubmits[s.Problem] = s.ID
			u.BestSubmitDate[s.Problem] = s.SubmitTime
		}
	}

	u.CalculateTotalScore()

	db.Save(&u)
}

func (sh JMapStrFloat64) Value() (driver.Value, error) {
	return json.Marshal(sh)
}

func (sh *JMapStrFloat64) Scan(value interface{}) error {
	b, ok := value.([]byte)
	if !ok {
		return json.Unmarshal(b, sh)
	}
	return json.Unmarshal(b, sh)
}

func (sh JMapStrString) Value() (driver.Value, error) {
	return json.Marshal(sh)
}

func (sh *JMapStrString) Scan(value interface{}) error {
	b, ok := value.([]byte)
	if !ok {
		return json.Unmarshal(b, sh)
	}
	return json.Unmarshal(b, sh)
}

func (sh JMapStrInt64) Value() (driver.Value, error) {
	return json.Marshal(sh)
}

func (sh *JMapStrInt64) Scan(value interface{}) error {
	b, ok := value.([]byte)
	if !ok {
		return json.Unmarshal(b, sh)
	}
	return json.Unmarshal(b, sh)
}
