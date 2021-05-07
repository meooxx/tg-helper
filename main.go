package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
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
	ADMIN = "administrator"
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
}

// path "/" handler
func handler(w http.ResponseWriter, r *http.Request) {
	updateID := r.URL.Query().Get("update_id")
	fmt.Println(updateID)
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
			fmt.Printf("Recovering from panic error is: %v \n", r)
		}
	}()
	var update Update
	err = json.Unmarshal(p, &update)
	if err != nil {
		panic(err)
	}

	newUsers := update.Message.NewChatMembers
	chat := update.Message.Chat
	entities := update.Message.Entities
	token := r.URL.Query().Get("token")
	if token == "" {
		panic(fmt.Errorf("no token"))
	}
	apiModel := ApiModel{Url: "https://api.telegram.org/bot", Token: token}

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
		case fmt.Sprintf("%s%s", COMMAND_KICKOFF, AT_MYSELF):
			fallthrough
		case COMMAND_KICKOFF:
			// 发送踢人指令的人是否有管理员权限
			if checkKickPermission(apiModel, update.Message.From.Id, chat.Id) {
				deleteMember(apiModel, relateUser.Mentioned.Id, chat.Id, update.Message.From.FirstName)
			}
		default:
			fmt.Print(parsedEnityKey)
			fmt.Println("无效的update")
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
	for _, entity := range entities {
		switch entity.Type {
		case BOT_COMMAND:
			fallthrough
		case MENTION:
			len := entity.Length
			offset := entity.Offset
			// 处理汉字 转成 rune 再转成 string
			entityName := string([]rune(msg.Text)[offset : len+offset])
			e[entityName] = &MentionedAndFrom{entity.User, msg.From, ""}
		// 踢人时候使用 /kickoff sombody
		// 第一次会被 BOT_COMMAND 匹配到 /kickoff
		// 第二次会被 TEXT_MENTION 匹配到 sombody
		// 所以第二次直接修改已经存储的信息
		case TEXT_MENTION:
			len := entity.Length
			offset := entity.Offset
			userName := string([]rune(msg.Text)[offset : len+offset])
			// 处理 /kickof@bot sb 这种形式的
			if preCommand, ok := e[COMMAND_KICKOFF]; ok {
				preCommand.Mentioned = entity.User
				preCommand.UserName = userName
			} else if preCommand, ok = e[fmt.Sprintf("%s%s", COMMAND_KICKOFF, AT_MYSELF)]; ok {
				preCommand.Mentioned = entity.User
				preCommand.UserName = userName
			}
		default:
			fmt.Println("invalid command")
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
		fmt.Printf("checkPermission request:%s", err)
	}
	by, _ := ioutil.ReadAll(res.Body)
	var j struct {
		ok     bool
		Result ChatMember `json:"result"`
	}
	fmt.Printf("%s", by)
	json.Unmarshal(by, &j)
	// 是创建者或者 admin 且 有踢人权限
	if j.Result.Status == CREATOR || (j.Result.Status == ADMIN && j.Result.CanRestrictMembers) {
		return true
	}
	return false
}

// 剔除用户从群组
func deleteMember(api ApiModel, userId, chatId int64, firstName string) {
	api.Method = "kickChatMember"
	params, _ := json.Marshal(map[string]int64{"user_id": userId, "chat_id": chatId})
	req, _ := http.NewRequest("POST", fmt.Sprintf("%s%s/%s", api.Url, api.Token, api.Method), bytes.NewBuffer(params))
	req.Header.Set("Content-Type", "application/json")
	client := clientWithWrapper()
	res, err := client.Do(req)
	if err != nil {
		fmt.Printf("%s", err)
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
		fmt.Printf("SendMessageParam stringify: %s", err)
		return SendMessageResult{}
	}
	req, err := http.NewRequest("POST", fmt.Sprintf("%s%s/%s", api.Url, api.Token, api.Method), bytes.NewBuffer(jsonByte))

	if err != nil {
		fmt.Printf("greet request:%s", err)
		return SendMessageResult{}
	}
	req.Header.Set("Content-Type", "application/json")
	client := clientWithWrapper()
	res, err := client.Do(req)
	if err != nil {
		fmt.Printf("do request: %s", err)
		return SendMessageResult{}
	}
	var snr SendMessageResult
	by, _ := ioutil.ReadAll(res.Body)
	err = json.Unmarshal(by, &snr)
	if err != nil {
		fmt.Printf("%v", err)
	}
	fmt.Printf("%s", by)
	defer res.Body.Close()
	return snr
}

// 增加 Timeout 和 Proxy
func clientWithWrapper() *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}}
}

var peddingDeleteMsg = make(chan MessageAndChatId)

func main() {
	http.HandleFunc("/update", handleUpdate)
	http.HandleFunc("/", handler)
	go deleteAfterFewDuration()
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
			fmt.Printf("greet request:%s", err)
		}
		res.Body.Close()
	}

}
