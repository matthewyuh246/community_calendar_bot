package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
	"github.com/robfig/cron"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"
	"google.golang.org/api/calendar/v3"
	"google.golang.org/api/option"
)

func loadEnv() {
	err := godotenv.Load(".env")

	if err != nil {
		fmt.Println(".env読み込みエラー: %v", err)
	}
	fmt.Println(".envを読み込みました。")
}

// Google Calendarの認証とクライアントのセットアップ
func getClient() (*calendar.Service, error) {
	b, err := os.ReadFile("credentials.json")
	if err != nil {
		log.Fatalf("Unable to read client secret file: %v", err)
	}

	config, err := google.ConfigFromJSON(b, calendar.CalendarReadonlyScope)
	if err != nil {
		log.Fatalf("Unable to parse client secret file to config: %v", err)
	}

	tokFile := "token.json"
	tok, err := tokenFromFile(tokFile)
	if err != nil {
		tok = getTokenFromWeb(config)
		saveToken(tokFile, tok)
	}
	client := config.Client(context.Background(), tok)
	return calendar.NewService(context.Background(), option.WithHTTPClient(client))
}

// Discordに次の日のGoogleカレンダーのイベントを通知する関数
func notifyNextDayEvents(s *discordgo.Session, calendarService *calendar.Service, calendarID string, channelID string) {
	now := time.Now()
	tomorrow := now.Add(24 * time.Hour)
	timeMin := time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 0, 0, 0, 0, tomorrow.Location()).Format("2006-01-02 15:04:05")
	timeMax := time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 23, 59, 59, 0, tomorrow.Location()).Format("2006-01-02 15:04:05")

	events, err := calendarService.Events.List(calendarID).ShowDeleted(false).
		SingleEvents(true).TimeMin(timeMin).TimeMax(timeMax).OrderBy("startTime").Do()
	if err != nil {
		log.Printf("Unable to retieve events: %v", err)
		return
	}

	if len(events.Items) == 0 {
		s.ChannelMessageSend(channelID, "次の日に予定はありません。")
		return
	}

	for _, item := range events.Items {
		start := item.Start.DateTime
		if start == "" {
			start = item.Start.Date
		}
		message := fmt.Sprintf("明日の予定: %s\n開始時刻: %s\nリンク: %s", item.Summary, start, item.HtmlLink)
		s.ChannelMessageSend(channelID, message)
	}
}

// DiscordにGoogleカレンダーイベントを通知する関数
func notifyDiscord(s *discordgo.Session, calendarService *calendar.Service, calendarID string, channelID string) {
	events, err := calendarService.Events.List(calendarID).ShowDeleted(false).
		SingleEvents(true).TimeMin(time.Now().Format("2006-01-02 15:04:05")).MaxResults(5).OrderBy("startTime").Do()
	if err != nil {
		log.Fatalf("Unable to retrieve events. %v", err)
	}

	if len(events.Items) == 0 {
		s.ChannelMessageSend(channelID, "現在、カレンダーに予定は登録されていません。")
		return
	}

	for _, item := range events.Items {
		var start string
		if item.Start.DateTime != "" {
			start = item.Start.DateTime
		} else {
			start = item.Start.Date
		}
		message := fmt.Sprintf("予定: %s\n開始時刻: %s\nリンク: %s", item.Summary, start, item.HtmlLink)
		s.ChannelMessageSend(channelID, message)
	}
}

func tokenFromFile(file string) (*oauth2.Token, error) {
	f, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	tok := &oauth2.Token{}
	err = json.NewDecoder(f).Decode(tok)
	return tok, err
}

func saveToken(path string, token *oauth2.Token) {
	f, err := os.Create(path)
	if err != nil {
		log.Fatalf("Unable to cache oauth token: %v", err)
	}
	defer f.Close()
	json.NewEncoder(f).Encode(token)
}

func getTokenFromWeb(config *oauth2.Config) *oauth2.Token {
	authURL := config.AuthCodeURL("state-token", oauth2.AccessTypeOffline)
	fmt.Printf("Go to the following link in your browser then type the authorization code: \n%v\n", authURL)

	var authCode string
	if _, err := fmt.Scan(&authCode); err != nil {
		log.Fatalf("Unable to read authorization code %v", err)
	}

	tok, err := config.Exchange(context.TODO(), authCode)
	if err != nil {
		log.Fatalf("Unable to retrieve token from web %v", err)
	}
	return tok
}

func main() {
	loadEnv()
	// Discord Botの初期化
	discordToken := os.Getenv("DISCORD_BOT_TOKEN")
	channelID := os.Getenv("DISCORD_CHANNEL_ID") // DiscordチャンネルID
	dg, err := discordgo.New("Bot " + discordToken)
	if err != nil {
		log.Fatalf("Error creating Discord session: %v", err)
	}
	defer dg.Close()

	// Google Calendar APIクライアントのセットアップ
	calendarService, err := getClient()
	if err != nil {
		log.Fatalf("Error creating Calendar client: %v", err)
	}

	calendarID := "primary"

	// Discordで "!events" コマンドが実行されたらGoogleカレンダーからイベントを取得して通知
	dg.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author.ID == s.State.User.ID {
			return
		}
		if m.Content == "!events" {
			notifyDiscord(s, calendarService, calendarID, channelID)
		}
	})
	//毎日21時に次の日の予定を取得するジョブを設定
	c := cron.New()
	c.AddFunc("0 21 * * *", func() {
		notifyNextDayEvents(dg, calendarService, calendarID, channelID)
	})
	c.Start()
	defer c.Stop()

	err = dg.Open()
	if err != nil {
		log.Fatalf("Error opening Discord session: %v", err)
	}
	defer dg.Close()
	fmt.Println("Bot稼働中。CTRL+Cで終了。")

	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc
}
