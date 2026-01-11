package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	tgbot "github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"github.com/joho/godotenv"

	"google.golang.org/api/option"
)

const (
	skipCommand = "/skip"
	cancelCmd   = "/cancel"
	startCmd    = "/start"
)

func main() {
	_ = godotenv.Load()

	token := mustEnv("TELEGRAM_BOT_TOKEN")
	sheetID := mustEnv("GOOGLE_SHEETS_ID")
	credsPath := mustEnv("GOOGLE_CREDENTIALS_FILE")
	allowedIDs := parseAllowedUserIDs(mustEnv("ALLOWED_USER_IDS"))

	ctx := context.Background()
  
	sheetsClient, err := NewSheetsClient(
		ctx,
		sheetID,
		option.WithCredentialsFile(credsPath),
		option.WithScopes("https://www.googleapis.com/auth/spreadsheets"),
	)

	if err != nil {
		log.Fatal(err)
	}

	store := NewStateStore()

	b, err := tgbot.New(token, tgbot.WithDefaultHandler(func(ctx context.Context, b *tgbot.Bot, update *models.Update) {
		log.Printf("UPDATE: %+v\n", update)
		if update.Message == nil {
			return
		}
	  log.Printf("MESSAGE: chat=%d text=%q\n", update.Message.Chat.ID, update.Message.Text)
		handleMessage(ctx, b, update.Message, store, sheetsClient, allowedIDs)
	}))
	if err != nil {
		log.Fatal(err)
	}

	log.Println("Bot started")
	b.Start(ctx)
}

func handleMessage(ctx context.Context, b *tgbot.Bot, msg *models.Message, store *StateStore, sheetsClient *SheetsClient, allowedIDs map[int64]struct{}) {
	userID := msg.From.ID
	
	if !isAllowed(allowedIDs, userID) {
	sendText(ctx, b, msg.Chat.ID, "🔒 Извините, у вас нет доступа к этому боту\\", nil)

	return
  }

	text := strings.TrimSpace(msg.Text)

	if text == cancelCmd {
		store.Reset(userID)
		sendText(ctx, b, msg.Chat.ID, "Ок, отменил\\. Чтобы начать заново — /start", nil)
		return
	}

	if text == startCmd {
		store.Reset(userID)
		st := store.Get(userID)
		st.Step = StepDate
		st.UpdatedAt = time.Now()
		sendText(ctx, b, msg.Chat.ID, "Введи *Дату* в формате DD\\.MM\\.YYYY \\(например 09\\.01\\.2026\\):\n\n❌ /cancel — отмена", &models.ReplyKeyboardRemove{RemoveKeyboard: true})
		return
	}

	st := store.Get(userID)

	if st.Step == StepNone {
		st.Step = StepDate
		sendText(ctx, b, msg.Chat.ID, "Начнём! Введи *Дату* в формате DD\\.MM\\.YYYY:\n\n❌ /cancel — отмена", &models.ReplyKeyboardRemove{RemoveKeyboard: true})
		return
	}

	cats, err := sheetsClient.GetCategories(ctx)
	if err != nil {
		sendText(ctx, b, msg.Chat.ID, "💀 Что-то сломалось: не смог прочитать лист «Категории»\\. Попробуй ещё раз\\.", nil)
		return
	}

	switch st.Step {
	case StepDate:
		if err := validateDateDDMMYYYY(text); err != nil {
			sendText(ctx, b, msg.Chat.ID, "😵‍💫 Дата должна быть в формате DD\\.MM\\.YYYY и не пустая\\. Пример: 09\\.01\\.2026", nil)
			return
		}
		st.Date = text
		st.Step = StepSpender
		sendText(ctx, b, msg.Chat.ID, "Выбери *На кого потратили*:", replyKeyboardFromList(cats.Spenders))
		return

	case StepSpender:
		if !contains(cats.Spenders, text) {
			sendText(ctx, b, msg.Chat.ID, "Выбери значение кнопкой ниже", replyKeyboardFromList(cats.Spenders))
			return
		}
		st.Spender = text
		st.Step = StepCategory
		sendText(ctx, b, msg.Chat.ID, "Выбери *Категория трат*:", replyKeyboardFromList(cats.Cats))
		return

	case StepCategory:
		if !contains(cats.Cats, text) {
			sendText(ctx, b, msg.Chat.ID, "Пожалуйста, выбери значение кнопкой ниже", replyKeyboardFromList(cats.Cats))
			return
		}
		st.Category = text
		st.Step = StepAmount
		sendText(ctx, b, msg.Chat.ID, "Введи *Сумму* \\(целое натуральное число\\):", &models.ReplyKeyboardRemove{RemoveKeyboard: true})
		return

	case StepAmount:
		amt, err := parsePositiveInt(text)
		if err != nil {
			sendText(ctx, b, msg.Chat.ID, "😵‍💫 Сумма должна быть целым натуральным числом \\(например 300\\)", nil)
			return
		}
		st.Amount = amt
		st.Step = StepComment
		sendText(ctx, b, msg.Chat.ID, fmt.Sprintf("Введи *Комментарий* или отправь %s чтобы пропустить:", skipCommand), nil)
		return

	case StepComment:
		if text == skipCommand {
			st.Comment = ""
		} else {
			st.Comment = text
		}
		st.Step = StepCard
		sendText(ctx, b, msg.Chat.ID, "Выбери *С чьей карты потратили*:", replyKeyboardFromList(cats.Cards))
		return

	case StepCard:
		if !contains(cats.Cards, text) {
			sendText(ctx, b, msg.Chat.ID, "Пожалуйста, выбери значение кнопкой ниже", replyKeyboardFromList(cats.Cards))
			return
		}
		st.Card = text

		err := sheetsClient.AppendExpenseRow(ctx, st.Date, st.Spender, st.Category, st.Amount, st.Card, st.Comment)
		if err != nil {
			sendText(ctx, b, msg.Chat.ID, "💀 Что-то сломалось: не смог записать в «Расходы»\\. Попробуй ещё раз\\.", nil)
			return
		}

		store.Reset(userID)
		sendText(ctx, b, msg.Chat.ID, "✅ Записал в «Расходы»\\.\n\nЧтобы добавить ещё — /start", &models.ReplyKeyboardRemove{RemoveKeyboard: true})
		return

	default:
		store.Reset(userID)
		sendText(ctx, b, msg.Chat.ID, "💀⌛ Состояние сбилось по таймауту или ещё по какой-то причине\\. Начнём заново? \\(/start\\)", &models.ReplyKeyboardRemove{RemoveKeyboard: true})
		return
	}
}

func sendText(ctx context.Context, b *tgbot.Bot, chatID int64, text string, replyMarkup any) {
	params := &tgbot.SendMessageParams{
		ChatID:    chatID,
		Text:      text,
		ParseMode: models.ParseModeMarkdown,
	}
	if replyMarkup != nil {
		params.ReplyMarkup = replyMarkup
	}

	_, err := b.SendMessage(ctx, params)
	if err != nil {
		log.Printf("SendMessage error: %v", err)
	}
}

func replyKeyboardFromList(items []string) *models.ReplyKeyboardMarkup {
	const perRow = 2
	rows := make([][]models.KeyboardButton, 0, (len(items)+perRow-1)/perRow)

	for i := 0; i < len(items); i += perRow {
		end := i + perRow
		if end > len(items) {
			end = len(items)
		}
		row := make([]models.KeyboardButton, 0, end-i)
		for _, it := range items[i:end] {
			row = append(row, models.KeyboardButton{Text: it})
		}
		rows = append(rows, row)
	}

	return &models.ReplyKeyboardMarkup{
		Keyboard:        rows,
		ResizeKeyboard:  true,
		OneTimeKeyboard: true,
	}
}

func validateDateDDMMYYYY(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("empty")
	}
	_, err := time.Parse("02.01.2006", s)
	return err
}

func parsePositiveInt(s string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return 0, errors.New("not positive int")
	}
	return n, nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func mustEnv(k string) string {
	v := strings.TrimSpace(os.Getenv(k))
	if v == "" {
		log.Fatalf("missing env: %s", k)
	}
	return v
}

func parseAllowedUserIDs(env string) map[int64]struct{} {
	out := make(map[int64]struct{})
	for _, part := range strings.Split(env, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			log.Fatalf("ALLOWED_USER_IDS contains non-integer: %q", part)
		}
		out[v] = struct{}{}
	}
	return out
}

func isAllowed(allowed map[int64]struct{}, userID int64) bool {
	_, ok := allowed[userID]
	return ok
}

