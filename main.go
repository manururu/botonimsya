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
	"github.com/go-telegram/ui/datepicker"
	"github.com/joho/godotenv"
	"google.golang.org/api/option"
)

const (
	skipCmd   = "/skip"
	cancelCmd = "/cancel"
	startCmd  = "/start"
	addCmd    = "/add"
	lastCmd   = "/last"

	dateFmt = "02.01.2006"
)

var dp *datepicker.DatePicker

func main() {
	_ = godotenv.Load()

	token := mustEnv("TELEGRAM_BOT_TOKEN")
	sheetID := mustEnv("GOOGLE_SHEETS_ID")
	sheetURL := fmt.Sprintf("https://docs.google.com/spreadsheets/d/%s/edit", sheetID)
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

	b, err := tgbot.New(
		token,
		tgbot.WithMessageTextHandler("", tgbot.MatchTypePrefix, func(ctx context.Context, b *tgbot.Bot, update *models.Update) {
			log.Printf("UPDATE: %+v\n", update)
			if update.Message == nil {
				return
			}
			log.Printf("MESSAGE: chat=%d text=%q\n", update.Message.Chat.ID, update.Message.Text)

			handleMessage(ctx, b, update.Message, store, sheetsClient, allowedIDs, sheetURL)
		}),

		tgbot.WithDefaultHandler(func(ctx context.Context, b *tgbot.Bot, update *models.Update) {
			if update.CallbackQuery != nil {
				log.Printf("CALLBACK: from=%d data=%q", update.CallbackQuery.From.ID, update.CallbackQuery.Data)
			}
		}),
	)
	if err != nil {
		log.Fatal(err)
	}

	dp = initUI(b, store, sheetsClient)
	log.Println("Bot started")
	b.Start(ctx)
}

func handleMessage(
	ctx context.Context,
	b *tgbot.Bot,
	msg *models.Message,
	store *StateStore,
	sheetsClient *SheetsClient,
	allowedIDs map[int64]struct{},
	sheetURL string,
) {
	userID := msg.Chat.ID

	if _, ok := allowedIDs[userID]; !ok {
		return
	}

	text := strings.TrimSpace(msg.Text)

	if text == startCmd {
		greeting := fmt.Sprintf(
			"Привет\\!\n\n"+
				"Я записываю семейные расходы в [таблицу](%s)\\.\n\n"+
				"➕ Добавить расход — /add\n"+
				"📋 Последние записи — /last\n"+
				"❌ Отменить ввод — /cancel\n",
			sheetURL,
		)
		sendText(ctx, b, msg.Chat.ID, greeting, nil)
		return
	}

	if text == cancelCmd {
		store.Reset(userID)
		sendText(ctx, b, msg.Chat.ID, "😕 Галя, у нас отмена\\. Чтобы начать заново — /add", nil)
		return
	}

	if text == lastCmd {
		submitter := string([]rune(msg.From.FirstName)[0])
		rows, err := sheetsClient.GetLastExpenses(ctx, submitter, 3)
		if err != nil {
			sendText(ctx, b, msg.Chat.ID, "💀 Не смог прочитать «Расходы»\\. Попробуй ещё раз\\.", nil)
			return
		}
		if len(rows) == 0 {
			sendText(ctx, b, msg.Chat.ID, "Записей пока нет\\.", nil)
			return
		}
		sendText(ctx, b, msg.Chat.ID, formatLastExpenses(rows), nil)
		return
	}

	if text == addCmd {
		store.Reset(userID)
		st := store.Get(userID)
		st.Step = StepDate
		st.UpdatedAt = time.Now()
		sendText(ctx, b, msg.Chat.ID,
			"Записываю ✍️\n\nВыбери *дату* на календаре:\n\n❌ /cancel — отмена",
			dp,
		)
		return
	}

	st := store.Get(userID)

	if st.Step == StepNone {
		sendText(ctx, b, msg.Chat.ID,
			"Чтобы добавить расход — отправь /add\n/start — справка",
			nil,
		)
		return
	}

	cats, err := sheetsClient.GetCategories(ctx)
	if err != nil {
		sendText(ctx, b, msg.Chat.ID, "💀 Что-то сломалось: не смог прочитать лист «Категории»\\. Попробуй ещё раз\\.", nil)
		return
	}

	switch st.Step {
	case StepDate:
		if err := validateDateDDMMYYYY(text); err == nil {
			st.Date = text
			st.Step = StepSpender
			sendText(ctx, b, msg.Chat.ID, "Выбери *На кого потратили*:", replyKeyboardFromList(cats.Spenders))
			return
		}

		sendText(ctx, b, msg.Chat.ID,
			"😵‍💫 Выбери *дату* на календаре (или введи DD.MM.YYYY):",
			dp,
		)
		return

	case StepSpender:
		if !contains(cats.Spenders, text) {
			sendText(ctx, b, msg.Chat.ID, "Выбери значение кнопкой ниже, не выдумывай 😹", replyKeyboardFromList(cats.Spenders))
			return
		}
		st.Spender = text
		st.Step = StepCategory
		sendText(ctx, b, msg.Chat.ID, "Выбери *Категория трат*:", replyKeyboardFromList(cats.Names))
		return

	case StepCategory:
		if !contains(cats.Names, text) {
			sendText(ctx, b, msg.Chat.ID, "Пожалуйста, выбери значение кнопкой ниже, зачем этот геморрой? 😹", replyKeyboardFromList(cats.Names))
			return
		}
		st.Category = text
		st.Step = StepAmount
		sendText(ctx, b, msg.Chat.ID, "Введи *Сумму* \\(число или выражение, например 300 или 100\\+200\\-50\\):", &models.ReplyKeyboardRemove{RemoveKeyboard: true})
		return

	case StepAmount:
		amt, err := parseAmountExpr(text)
		if err != nil {
			sendText(ctx, b, msg.Chat.ID, "😵‍💫 Сумма должна быть числом или выражением \\(например 300 или 100\\+200\\-50\\)", nil)
			return
		}
		st.Amount = amt
		st.Step = StepComment
		sendText(ctx, b, msg.Chat.ID, fmt.Sprintf("Введи *Комментарий* или отправь %s чтобы пропустить:", skipCmd), nil)
		return

	case StepComment:
		if text == skipCmd {
			st.Comment = ""
		} else {
			st.Comment = text
		}
		st.Step = StepSubmitter
		submitExpense(ctx, b, msg, st, sheetsClient, store)
		return

	default:
		store.Reset(userID)
		sendText(ctx, b, msg.Chat.ID, "💀⌛ Состояние сбилось по таймауту или ещё по какой-то причине\\. Начнём заново? \\(/add\\)", &models.ReplyKeyboardRemove{RemoveKeyboard: true})
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
	_, err := time.Parse(dateFmt, s)
	return err
}

func parseAmountExpr(s string) (int, error) {
	s = strings.TrimSpace(s)
	// нормализуем знаки: каждое число будет предварено + или -, затем разбиваем по +
	s = strings.ReplaceAll(s, "-", "+-")
	parts := strings.Split(s, "+")
	total := 0
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return 0, errors.New("invalid amount")
		}
		total += n
	}
	if total <= 0 {
		return 0, errors.New("invalid amount")
	}
	return total, nil
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func padRight(s string, width int) string {
	r := []rune(s)
	if len(r) >= width {
		return s
	}
	return s + strings.Repeat(" ", width-len(r))
}

func padLeft(s string, width int) string {
	r := []rune(s)
	if len(r) >= width {
		return s
	}
	return strings.Repeat(" ", width-len(r)) + s
}

func runeLen(s string) int {
	return len([]rune(s))
}

func formatLastExpenses(rows []ExpenseRow) string {
	const hDate, hCat, hAmt, hComment = "Дата", "Категория", "Сумма", "Комментарий"

	wDate := runeLen(hDate)
	wCat := runeLen(hCat)
	wAmt := runeLen(hAmt)
	wComment := runeLen(hComment)

	for _, r := range rows {
		if w := runeLen(r.Date); w > wDate {
			wDate = w
		}
		if w := runeLen(r.Category); w > wCat {
			wCat = w
		}
		if w := runeLen(r.Amount); w > wAmt {
			wAmt = w
		}
		comment := r.Comment
		if comment == "" {
			comment = "—"
		}
		if w := runeLen(comment); w > wComment {
			wComment = w
		}
	}

	sepDate := strings.Repeat("-", wDate)
	sepCat := strings.Repeat("-", wCat)
	sepAmt := strings.Repeat("-", wAmt)
	sepComment := strings.Repeat("-", wComment)

	row := func(date, cat, amt, comment string) string {
		return padRight(date, wDate) + " | " +
			padRight(cat, wCat) + " | " +
			padLeft(amt, wAmt) + " | " +
			padRight(comment, wComment)
	}

	var sb strings.Builder
	sb.WriteString("```\n")
	sb.WriteString(row(hDate, hCat, hAmt, hComment))
	sb.WriteString("\n")
	sb.WriteString(sepDate + "-+-" + sepCat + "-+-" + sepAmt + "-+-" + sepComment)
	sb.WriteString("\n")
	for _, r := range rows {
		comment := r.Comment
		if comment == "" {
			comment = "—"
		}
		sb.WriteString(row(r.Date, r.Category, r.Amount, comment))
		sb.WriteString("\n")
	}
	sb.WriteString("```")

	return sb.String()
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

func submitExpense(
	ctx context.Context,
	b *tgbot.Bot,
	msg *models.Message,
	st *UserState,
	sheetsClient *SheetsClient,
	store *StateStore,
) {
	sendText(ctx, b, msg.Chat.ID, "Смотрю, *кто внёс данную запись о расходах*\\.\\.\\.", nil)

	userName := msg.From.FirstName
	firstLetter := string([]rune(userName)[0])
	st.Submitter = firstLetter

	sendText(ctx, b, msg.Chat.ID, fmt.Sprintf("Ага\\! Наконец\\-то это *%s*\\!", userName), nil)

	err := sheetsClient.AppendExpenseRow(ctx, st.Date, st.Spender, st.Category, st.Amount, st.Submitter, st.Comment)
	if err != nil {
		sendText(ctx, b, msg.Chat.ID, "💀 Что-то сломалось: не смог записать в «Расходы»\\. Попробуй ещё раз\\.", nil)
		return
	}

	store.Reset(msg.Chat.ID)
	sendText(ctx, b, msg.Chat.ID, "✅ Записал в «Расходы»\\.\n\nЧтобы добавить ещё — /add", &models.ReplyKeyboardRemove{RemoveKeyboard: true})
}

func initUI(
	b *tgbot.Bot,
	store *StateStore,
	sheets *SheetsClient,
) *datepicker.DatePicker {
	deletePickerMessage := func(ctx context.Context, b *tgbot.Bot, mes models.MaybeInaccessibleMessage) {
		if mes.Message == nil {
			return
		}
		if _, err := b.DeleteMessage(ctx, &tgbot.DeleteMessageParams{
			ChatID:    mes.Message.Chat.ID,
			MessageID: mes.Message.ID,
		}); err != nil {
			log.Printf("DATEPICKER delete message error: %v", err)
		}
	}

	handler := func(
		ctx context.Context,
		b *tgbot.Bot,
		mes models.MaybeInaccessibleMessage,
		date time.Time,
	) {
		deletePickerMessage(ctx, b, mes)

		if mes.Message == nil {
			return
		}

		msg := mes.Message
		userID := msg.Chat.ID

		st := store.Get(userID)
		if st.Step != StepDate {
			return
		}

		st.Date = date.Format(dateFmt)
		st.Step = StepSpender
		st.UpdatedAt = time.Now()

		log.Printf("DATEPICKER select: chat=%d date=%s", userID, st.Date)

		cats, err := sheets.GetCategories(ctx)
		if err != nil {
			sendText(ctx, b, msg.Chat.ID, "💀 Ошибка чтения категорий", nil)
			return
		}

		sendText(ctx, b, msg.Chat.ID,
			"Выбери *На кого потратили*:",
			replyKeyboardFromList(cats.Spenders),
		)
	}

	cancelHandler := func(ctx context.Context, b *tgbot.Bot, mes models.MaybeInaccessibleMessage) {
		if mes.Message == nil {
			return
		}

		userID := mes.Message.Chat.ID
		st := store.Get(userID)
		if st.Step != StepDate {
			return
		}

		deletePickerMessage(ctx, b, mes)
		store.Reset(userID)
		sendText(ctx, b, userID, "😕 Галя, у нас отмена\\. Чтобы начать заново — /add", &models.ReplyKeyboardRemove{RemoveKeyboard: true})
	}

	return datepicker.New(
		b,
		handler,
		datepicker.WithPrefix("date"),
		datepicker.Language("ru"),
		datepicker.NoDeleteAfterSelect(),
		datepicker.NoDeleteAfterCancel(),
		datepicker.OnCancel(cancelHandler),
	)
}
