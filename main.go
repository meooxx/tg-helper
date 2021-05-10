package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	MENTION      = "mention"
	BOT_COMMAND  = "bot_command"
	TEXT_MENTION = "text_mention"
	// escaped '.', is requred by tg markdown -> '\.',
	// escaped '\' , is required by go, so '\\.'
	REPO            = "https://github\\.com/xxxXXX95/yuyue"
	COMMAND_REPO    = "/repo"
	COMMAND_HELP    = "/help"
	COMMAND_KICKOFF = "/kickoff"
	// robot name
	AT_MYSELF = "@github_release_notify_bot"
	// 创建者
	CREATOR = "creator"
	// 管理员
	ADMIN  = "administrator"
	TG_API = "https://api.telegram.org/bot"
)

type User struct {
	Id        int64  `json:"id"`
	IsBot     bool   `json:"is_bot"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
}

type Message struct {
	MessageId      int64           `json:"message_id"`
	From           User            `json:"from"`
	Text           string          `json:"text"`
	Entities       []MessageEntity `json:"entities"`
	Chat           Chat            `json:"chat"`
	NewChatMembers []User          `json:"new_chat_members"`
}

type Chat struct {
	Id int64 `json:"id"`
}

type ChatMember struct {
	Chat               Chat   `json:"chat"`
	User               User   `json:"user"`
	CanRestrictMembers bool   `json:"can_restrict_members"`
	Status             string `json:"status"`
}

type ChatMemberUpdated struct {
	NewChatMember ChatMember `json:"new_chat_member"`
}

type MessageEntity struct {
	Type   string `json:"type"`
	Offset uint8  `json:"offset"`
	Length uint8  `json:"length"`
	User   User   `json:"user"`
}

type Update struct {
	UpdateID int64 `json:"update_id"`
	// ChatMember ChatMemberUpdated `json:"chat_member"`
	Message Message `json:"message"`
	// 编辑过的 message
	EditedMessage Message `json:"edited_message"`
}

// path "/" handler
func handler(w http.ResponseWriter, r *http.Request) {
	updateID := r.URL.Query().Get("update_id")
	log.Println(updateID)
	w.Write([]byte("hello!"))
}

// path "update" handler
func handleUpdate(w http.ResponseWriter, r *http.Request) {
	// should POST method
	if r.Method != http.MethodPost {
		// http.StatusMethodNotAllowed
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Write([]byte("err"))
		return
	}
	p, err := ioutil.ReadAll(r.Body)
	if err != nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("Recovering from panic error is: %v \n", r)
		}
	}()
	var update Update
	err = json.Unmarshal(p, &update)
	if err != nil {
		panic(err)
	}
	// 如果有 edited_message 当作 messsage 处理
	if update.EditedMessage.MessageId != 0 {
		update.Message = update.EditedMessage
	}
	newUsers := update.Message.NewChatMembers
	chat := update.Message.Chat
	entities := update.Message.Entities
	token := r.URL.Query().Get("token")
	if token == "" {
		panic(fmt.Errorf("no token"))
	}
	apiModel := ApiModel{Url: TG_API, Token: token}

	var metionNames string
	for _, user := range newUsers {
		metionNames += fmt.Sprintf(", [@%s](tg://user?id=%d)", user.FirstName, user.Id)
	}

	// for new User
	if len(metionNames) != 0 {
		text := fmt.Sprintf("hello%s, 需要帮助请输入'/'获取机器人命令", metionNames)
		sendTgMessage(apiModel, text, chat.Id)
		return
	}
	parsedEnities := parseEntities(entities, update.Message)
	for parsedEnityKey, relateUser := range parsedEnities {
		switch parsedEnityKey {
		case AT_MYSELF:
			text := fmt.Sprintf("@我干嘛, 需要帮助请@群主或者输入'/'\n[@%s](tg://user?id=%d)", relateUser.From.FirstName, relateUser.From.Id)
			sendTgMessage(apiModel, text, chat.Id)
		// /repo@github_release_bot 这种形式
		case fmt.Sprintf("%s%s", COMMAND_REPO, AT_MYSELF):
			fallthrough
		case COMMAND_REPO:
			res := sendTgMessage(apiModel, fmt.Sprintf("[%s](%s)\n 为了防止信息太频繁此消息%ds后删除", REPO, REPO, 30), chat.Id)
			// x 秒后删除此信息
			peddingDeleteMsg <- MessageAndChatId{chat.Id, res.Result.MessageId, apiModel, 30}
		case fmt.Sprintf("%s%s", COMMAND_HELP, AT_MYSELF):
			fallthrough
		case COMMAND_HELP:
			sendTgMessage(apiModel, fmt.Sprintf("%s 获取仓库地址\n%s 踢人请@群主", COMMAND_REPO, COMMAND_KICKOFF), chat.Id)
		case COMMAND_KICKOFF:
			// 发送踢人指令的人是否有管理员权限
			if checkKickPermission(apiModel, update.Message.From.Id, chat.Id) {
				deleteMember(apiModel, relateUser.Mentioned.Id, chat.Id)
			}
		default:
			log.Print(parsedEnityKey)
			log.Println("无效的update")
		}

	}
}

type ApiModel struct {
	Token  string
	Url    string
	Method string // tg method eg.getMem, sendMessage...
}

// send a message should set these field
type SendMessageParam struct {
	ChatId    int64  `json:"chat_id"`
	Text      string `json:"text"`
	ParseMode string `json:"parse_mode"`
}

// 用于删除用户,回复 @用户
type MentionedAndFrom struct {
	Mentioned User
	From      User
	UserName  string
}

// 用于删除信息
type MessageAndChatId struct {
	ChatId    int64
	MessageId int64
	api       ApiModel
	Duration  uint8
}

// 将 enities 转换成 key values 形式
// 只有 @ 的时候会有 user
// 只有删除 用户的时候 会有 from 和 user
func parseEntities(entities []MessageEntity, msg Message) map[string]*MentionedAndFrom {
	e := map[string]*MentionedAndFrom{}
	var textMentioned *MentionedAndFrom
	for _, entity := range entities {
		len := entity.Length
		offset := entity.Offset
		switch entity.Type {
		case BOT_COMMAND:
			// 处理汉字 转成 rune 再转成 string
			entityName := string([]rune(msg.Text)[offset : len+offset])
			e[entityName] = &MentionedAndFrom{entity.User, msg.From, ""}
		case MENTION:
			// 处理汉字 转成 rune 再转成 string
			// entityName := string([]rune(msg.Text)[offset : len+offset])
			e[AT_MYSELF] = &MentionedAndFrom{entity.User, msg.From, ""}
			if textMentioned != nil {
				textMentioned.From = msg.From
				e[COMMAND_KICKOFF] = textMentioned
				delete(e, AT_MYSELF)
			}
		case TEXT_MENTION:
			userName := string([]rune(msg.Text)[offset : len+offset])
			if _, ok := e[AT_MYSELF]; ok {
				e[COMMAND_KICKOFF] = &MentionedAndFrom{entity.User, msg.From, userName}
				delete(e, AT_MYSELF)
			} else {
				textMentioned = &MentionedAndFrom{entity.User, msg.From, userName}
			}
		default:
			log.Printf("invalid command:%v", entity)
		}
	}
	return e
}

// 检查发送 /kick 命令是否有权限
func checkKickPermission(api ApiModel, userId, chatId int64) bool {
	api.Method = "getChatMember"
	params, _ := json.Marshal(map[string]int64{"chat_id": chatId, "user_id": userId})
	req, _ := http.NewRequest("POST", fmt.Sprintf("%s%s/%s", api.Url, api.Token, api.Method), bytes.NewBuffer(params))
	req.Header.Set("Content-Type", "application/json")
	client := clientWithWrapper()
	res, err := client.Do(req)
	if err != nil {
		log.Printf("checkPermission request:%s", err)
	}
	by, _ := ioutil.ReadAll(res.Body)
	var j struct {
		ok     bool
		Result ChatMember `json:"result"`
	}
	log.Printf("%s", by)
	json.Unmarshal(by, &j)
	// 是创建者或者 admin 且 有踢人权限
	if j.Result.Status == CREATOR || (j.Result.Status == ADMIN && j.Result.CanRestrictMembers) {
		return true
	}
	return false
}

// 剔除用户从群组
func deleteMember(api ApiModel, userId, chatId int64) {
	api.Method = "kickChatMember"
	params, _ := json.Marshal(map[string]int64{"user_id": userId, "chat_id": chatId})
	req, _ := http.NewRequest("POST", fmt.Sprintf("%s%s/%s", api.Url, api.Token, api.Method), bytes.NewBuffer(params))
	req.Header.Set("Content-Type", "application/json")
	client := clientWithWrapper()
	res, err := client.Do(req)
	if err != nil {
		log.Printf("%s", err)
	}
	defer res.Body.Close()
}

// func runCommand(cmd string) {
// 	switch cmd {
// 	case COMMAND_REPO:
// 		sendTgMessage()
// 	}
// }

type SendMessageResult struct {
	Ok     bool
	Result Message `json:"result"`
}

func sendTgMessage(api ApiModel, text string, chatId int64) SendMessageResult {
	api.Method = "sendMessage"
	param := SendMessageParam{ChatId: chatId, Text: text, ParseMode: "MarkdownV2"}
	jsonByte, err := json.Marshal(param)
	if err != nil {
		log.Printf("SendMessageParam stringify: %s", err)
		return SendMessageResult{}
	}
	req, err := http.NewRequest("POST", fmt.Sprintf("%s%s/%s", api.Url, api.Token, api.Method), bytes.NewBuffer(jsonByte))

	if err != nil {
		log.Printf("greet request:%s", err)
		return SendMessageResult{}
	}
	req.Header.Set("Content-Type", "application/json")
	client := clientWithWrapper()
	res, err := client.Do(req)
	if err != nil {
		log.Printf("do request: %s", err)
		return SendMessageResult{}
	}
	var snr SendMessageResult
	by, _ := ioutil.ReadAll(res.Body)
	err = json.Unmarshal(by, &snr)
	if err != nil {
		log.Printf("%v", err)
	}
	defer res.Body.Close()
	return snr
}

// 增加 Timeout 和 Proxy
func clientWithWrapper() *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
}

// jd miaosha response type
type Group struct {
	Gid       uint8  `json:"gid"`
	GroupTime string `json:"groupTime"`
}
type Miaosha struct {
	ShortWname string `json:"shortWname"`
	WareId     string `json:"wareId"`
	// imageurl      string
	JdPrice       string `json:"jdPrice"`
	MiaoShaPrice  string `json:"miaoShaPrice"`
	StartTimeShow string `json:"startTimeShow"`
}
type MiaoshaListJson struct {
	Groups      []Group   `json:"groups"`
	MiaoShaList []Miaosha `json:"miaoShaList"`
	Gid         string    `json:"gid"`
}

func getMiaoshaList(gid uint8) MiaoshaListJson {
	v := url.Values{}
	v.Add("appid", "o2_channels")
	v.Add("functionId", "pcMiaoShaAreaList")
	v.Add("client", "pc")
	v.Add("clientVersion", "1.0.0")
	v.Add("callback", "pcMiaoShaAreaList")
	v.Add("jsonp", "pcMiaoShaAreaList")
	if gid == 0 {
		v.Add("body", "{}")
	} else {
		v.Add("body", fmt.Sprintf("{gid:%d}", gid))
	}
	v.Add("_", fmt.Sprint(time.Now().Unix()))
	q := v.Encode()
	req, _ := http.NewRequest("GET", fmt.Sprintf("https://api.m.jd.com/api?%s", q), nil)
	req.Header.Add("referer", "https://miaosha.jd.com/")
	req.Header.Add("user-agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/90.0.4430.93 Safari/537.36")
	client := clientWithWrapper()
	res, err := client.Do(req)
	if err != nil {
		log.Println("秒杀请求失败")
	}
	defer res.Body.Close()
	body, _ := ioutil.ReadAll(res.Body)
	// fn({}); -> {}
	body = body[len("pcMiaoShaAreaList")+1 : len(body)-2]
	var msList MiaoshaListJson
	_ = json.Unmarshal(body, &msList)
	return msList
}

func RunScheduleJob() {
	ticker := time.NewTicker(30 * time.Minute)
	//ticker := time.NewTicker(5 * time.Second)
	type Job struct {
		Base       time.Time
		StartOfDay time.Time
		EndOfDay   time.Time
		At         time.Time
	}

	SetNextRunTime := func(j *Job) *Job {
		switch {
		// 今天第一次为空
		case j.StartOfDay.IsZero():
			j.StartOfDay = j.Base.Add(7 * time.Hour)
			j.At = j.StartOfDay
		// 今天第二次为空
		case j.EndOfDay.IsZero():
			j.EndOfDay = j.Base.Add(17 * time.Hour)
			j.At = j.EndOfDay
		// 切到明天
		default:
			nextBase := j.Base.Add(1 * time.Hour * 24)
			*j = Job{}
			j.Base = nextBase
			j.StartOfDay = nextBase.Add(7 * time.Hour)
			j.At = j.StartOfDay
		}
		return j
	}

	// 监控秒杀信息
	SpyOnJdMiaosha := func() {
		defer func() {
			if ok := recover(); ok != nil {
				log.Println("recover from schedule job:", ok)
			}
		}()
		// 过滤 5折 或者低于 15块 的商品
		FilterGoods := func(l []Miaosha, maxPrice float64, minDisCount float64) []Miaosha {
			var r []Miaosha
			for _, good := range l {
				jdPrice, _ := strconv.ParseFloat(good.MiaoShaPrice, 32)
				originPrice, _ := strconv.ParseFloat(good.JdPrice, 32)
				discount := jdPrice / originPrice
				if jdPrice < maxPrice || discount < minDisCount {
					r = append(r, good)
				}
			}
			return r
		}
		miaosha := getMiaoshaList(0)
		groupSku := []Miaosha{}

		goodsList := FilterGoods(miaosha.MiaoShaList, 15, 0.2)
		groupSku = append(groupSku, goodsList...)
		for _, g := range miaosha.Groups {
			if fmt.Sprint(g.Gid) != miaosha.Gid {
				miaosha = getMiaoshaList(g.Gid)
				goodsList = FilterGoods(miaosha.MiaoShaList, 15, 0.2)
				groupSku = append(groupSku, goodsList...)
			}
			time.Sleep(1 * time.Second)
		}
		if len(groupSku) == 0 {
			log.Println("没有找到合适的商品")
			return
		}
		apiModel := ApiModel{authInfo.Token, TG_API, "sendMessage"}
		text := "兄弟们,冲优惠2折和15元以下商品\n"
		for _, item := range groupSku {
			// markdown 转译. \., golang 转译 \\.
			itemUrl := fmt.Sprintf("item\\.jd\\.com/%s\\.html", item.WareId)
			escapedPrice := strings.Replace(item.MiaoShaPrice, ".", "\\.", 1)
			// [18:00]xxx商品-价格-sku
			text += fmt.Sprintf("[\\[%s\\]%s\\-%s元\\-%s](%s)\n", item.StartTimeShow, item.ShortWname, itemUrl, escapedPrice, item.WareId)
		}
		sendTgMessage(apiModel, text, authInfo.ChatId)
	}

	// set up job
	now := time.Now()
	t := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	// 一天俩次请求  7 点和 17 点
	startTime := t.Add(7 * time.Hour)
	deadlineTime := t.Add(17 * time.Hour)
	var job Job
	job.Base = t
	// next day
	if now.After(deadlineTime) {
		t = t.Add(1 * time.Hour * 24)
		job.Base = t
	} else if now.After(startTime) {
		job.StartOfDay = startTime
	}
	go func() {
		for {
			<-ticker.C
			if job.At.IsZero() {
				SetNextRunTime(&job)
				log.Println("Next run time:", job.At.String())
			}
			if time.Now().After(job.At) {
				// _ = SpyOnJdMiaosha
				SpyOnJdMiaosha()
				job.At = time.Time{}
			}
		}
	}()
}

// 待删除的 msg 请求参数
var peddingDeleteMsg = make(chan MessageAndChatId)
var authInfo struct {
	Token  string
	ChatId int64
}

func init() {
	data, err := ioutil.ReadFile("config.env")
	if err != nil {
		log.Println("read config error", err)
	}
	// a=b \n c=d
	// ["a=b", "c=d"]
	tb := bytes.Fields(data)
	m := map[string][]byte{}
	for _, field := range tb {
		keyValue := bytes.Split(field, []byte("="))
		m[string(keyValue[0])] = keyValue[1]
	}
	authInfo.Token = string(m["token"])
	chatId, _ := strconv.ParseInt(string(m["chatId"]), 10, 64)
	authInfo.ChatId = chatId
	log.Println(authInfo)
}

func main() {
	http.HandleFunc("/update", handleUpdate)
	http.HandleFunc("/", handler)
	// 异步删除消息函数
	go deleteAfterFewDuration()
	// 定期去查 秒杀商品
	go RunScheduleJob()
	log.Fatal(http.ListenAndServe(":8080", nil))
}

// 延迟一段时间后删除 channel 里面的信息
func deleteAfterFewDuration() {
	for {
		peddingDelMsg := <-peddingDeleteMsg
		time.Sleep(15 * time.Second)
		peddingDelMsg.api.Method = "deleteMessage"
		params, _ := json.Marshal(map[string]int64{"chat_id": peddingDelMsg.ChatId, "message_id": peddingDelMsg.MessageId})
		req, _ := http.NewRequest("POST", fmt.Sprintf("%s%s/%s", peddingDelMsg.api.Url, peddingDelMsg.api.Token, peddingDelMsg.api.Method), bytes.NewBuffer(params))
		req.Header.Set("Content-Type", "application/json")
		client := clientWithWrapper()
		res, err := client.Do(req)
		if err != nil {
			log.Printf("greet request:%s", err)
		}
		res.Body.Close()
	}

}
