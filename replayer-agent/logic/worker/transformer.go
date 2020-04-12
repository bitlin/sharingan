package worker

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"

	"github.com/didichuxing/sharingan/replayer-agent/model/esmodel"
	"github.com/didichuxing/sharingan/replayer-agent/model/recording"
	"github.com/didichuxing/sharingan/replayer-agent/model/replaying"
	"github.com/didichuxing/sharingan/replayer-agent/utils/helper"
)

//TODO: 处理redis数据存map
type Transformer struct {
	//apcuKeyIdx map[string]int // to filter redundant req of redis get
}

func (t *Transformer) BuildSessions(sessions []esmodel.Session) ([]*replaying.Session, error) {
	replayingSessions := make([]*replaying.Session, 0, len(sessions))

	for _, session := range sessions {
		replayingSession := replaying.NewSession()

		replayingSession.Context = strings.Trim(session.Context, " ")
		replayingSession.SessionId = session.SessionId
		replayingSession.CallFromInbound = t.buildCallFromInBound(session.CallFromInbound)
		replayingSession.ReturnInbound = t.buildReturnInBound(session.ReturnInbound, session.Actions)
		replayingSession.MockFiles = t.buildMockFiles(session.Actions)
		replayingSession.CallOutbounds = t.buildCallOutBound(session.Actions)
		replayingSession.AppendFiles = t.buildAppendFile(session.Actions)

		replayingSessions = append(replayingSessions, replayingSession)
	}

	return replayingSessions, nil
}

func (t *Transformer) buildCallFromInBound(callFromInBound *esmodel.CallFromInbound) *recording.CallFromInbound {
	inbound := recording.CallFromInbound{
		Request: callFromInBound.Request.Data,
		Raw:     callFromInBound.Request.Data,
	}

	inbound.SetOccurredAt(callFromInBound.OccurredAt)
	return &inbound
}

func (t *Transformer) buildReturnInBound(returnInBound *esmodel.ReturnInbound, actions []esmodel.Action) *recording.ReturnInbound {
	if returnInBound == nil {
		return nil
	}

	response := returnInBound.Response.Data
	if response == nil {
		for _, action := range actions {
			if action.ActionMeta.ActionType == "ReturnInbound" {
				response = action.Response.Data
				break
			}
		}
	}

	return &recording.ReturnInbound{
		Response: returnInBound.Response.Data,
		Raw:      returnInBound.Response.Data,
	}
}

type Rule struct {
	Subject string     `json:"subject"`
	Verb    string     `json:"verb"`
	Objects [][]string `json:"objects"`
}
type Group struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Rule        Rule   `json:"rule"`
}
type Experiment struct {
	Groups []Group `json:"groups"`
}
type Toggle struct {
	Namespace      string     `json:"namespace"`
	Name           string     `json:"name"`
	Version        int        `json:"version"`
	LastModifyTime int64      `json:"last_modify_time"`
	LogRate        int        `json:"log_rate"`
	Rule           Rule       `json:"rule"`
	Experiment     Experiment `json:"experiment"`
	PublishTo      []string   `json:"publish_to"`
	SchemaVersion  string     `json:"schema_version"`
}
type MockData struct {
	Toggle Toggle `json:"toggle"`
}

//TODO:对外是否删掉
func (t *Transformer) buildMockFiles(actions []esmodel.Action) map[string][][]byte {
	m := make(map[string][][]byte)
	for _, action := range actions {
		if action.ActionType != "SendUDP" || action.Peer.IP.String() != "127.0.0.1" || action.Peer.Port != 9891 {
			continue
		}
		splits := strings.Split(string(action.Content.Data), "\t")
		if splits == nil || len(splits) <= 3 || splits[0] != "1" {
			continue
		}

		toggleName := splits[1]
		toggleStatus := splits[2]

		contents, ok := m[toggleName]
		if !ok {
			contents = make([][]byte, 0)
		}

		object := make([]string, 0, 2)
		if toggleStatus == "1" {
			object = append(object, time.Now().AddDate(-1, 0, 0).Format("2006-01-02"))
			object = append(object, time.Now().AddDate(1, 0, 0).Format("2006-01-02"))
		} else {
			object = append(object, time.Now().AddDate(1, 0, 0).Format("2006-01-02"))
			object = append(object, time.Now().Format("2006-01-02"))
		}
		objects := make([][]string, 0, 1)
		objects = append(objects, object)
		experiment := Experiment{}
		if len(splits) > 3 {
			names := strings.Split(splits[3], ":")
			experiment.Groups = []Group{Group{
				Name: names[len(names)-1],
				Rule: Rule{
					Subject: "date_time_period",
					Verb:    "=",
					Objects: objects,
				},
			}}
		}
		mockData := MockData{
			Toggle: Toggle{
				Namespace:      "gs_api",
				Name:           toggleName,
				Version:        0,
				LastModifyTime: time.Now().Unix(),
				LogRate:        0,
				Rule: Rule{
					Subject: "date_time_period",
					Verb:    "=",
					Objects: objects,
				},
				Experiment:    experiment,
				SchemaVersion: "1.4.0",
			},
		}
		dm, _ := json.Marshal(mockData)

		contents = append(contents, dm)
		m[toggleName] = contents
	}
	return m
}

func (t *Transformer) buildCallOutBound(actions []esmodel.Action) []*recording.CallOutbound {
	outbounds := make([]*recording.CallOutbound, 0)
	for _, action := range actions {
		switch action.ActionType {
		case "CallOutbound":
			act := &recording.CallOutbound{
				Peer:         action.Peer,
				ResponseTime: action.ResponseTime,
				Request:      action.Request.Data,
				Response:     action.Response.Data,
				SocketFD:     action.SocketFD,
			}
			act.SetActionIndex(action.ActionIndex)
			act.SetActionType(action.ActionType)
			act.SetOccurredAt(action.OccurredAt)

			// auto remove the redundant prefix
			AutoFormatRedis(act)

			outbounds = append(outbounds, act)
		}
	}
	// for curl_multi
	outbounds = UniformCurlMulti(outbounds)
	return outbounds
}

func AutoFormatRedis(act *recording.CallOutbound) {
	if bytes.HasPrefix(act.Request, []byte("**")) && bytes.HasPrefix(act.Response, []byte("$")) {
		act.Request = act.Request[1:]
	}
	if bytes.HasPrefix(act.Request, []byte("**")) && bytes.HasPrefix(act.Response, []byte(":")) {
		act.Request = act.Request[1:]
	}
	if bytes.HasPrefix(act.Request, []byte("**")) && bytes.HasPrefix(act.Response, []byte("*")) {
		act.Request = act.Request[1:]
	}
	if bytes.HasPrefix(act.Request, []byte("*")) && bytes.HasPrefix(act.Response, []byte("$$")) {
		act.Response = act.Response[1:]
	}
	if bytes.HasPrefix(act.Request, []byte("*")) && bytes.HasPrefix(act.Response, []byte("::")) {
		act.Response = act.Response[1:]
	}
	if bytes.HasPrefix(act.Request, []byte("*")) && bytes.HasPrefix(act.Response, []byte("**")) {
		act.Response = act.Response[1:]
	}
}

func UniformCurlMulti(outbound []*recording.CallOutbound) []*recording.CallOutbound {
	total := len(outbound)
	for i := 0; i < total; i++ {
		if len(outbound[i].Response) != 0 || len(outbound[i].Request) < 20 {
			continue
		}
		j := i + 1
		for ; j < total; j++ {
			if len(outbound[j].Request) == 0 && bytes.HasPrefix(outbound[j].Response, []byte("HTTP/")) &&
				outbound[j].SocketFD == outbound[i].SocketFD && outbound[i].Peer.String() == outbound[j].Peer.String() {
				break
			}
		}
		if j >= total {
			continue
		}
		// make pair of req and resp
		outbound[i].Response = outbound[j].Response
		for ; j < total-1; j++ {
			outbound[j] = outbound[j+1]
		}
		total--
	}
	return outbound[:total]
}

func (t *Transformer) buildAppendFile(actions []esmodel.Action) []*recording.AppendFile {
	appendFiles := make([]*recording.AppendFile, 0)
	for _, action := range actions {
		switch action.ActionType {
		case "AppendFile":
			afs := bytes.Split(action.Content.Data, []byte{'\n'})
			for _, af := range afs {
				if len(af) == 0 || !strings.Contains(helper.BytesToString(af), "opera_stat_key") {
					continue
				}
				appendFile := &recording.AppendFile{
					Content: af,
				}
				appendFiles = append(appendFiles, appendFile)
			}
		}
	}
	return appendFiles
}