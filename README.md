# BOTonimsya
Personal Telegram bot to track income and expenses in Google Sheets

### Dependencies
- github.com/go-telegram/bot
- github.com/go-telegram/ui
- github.com/joho/godotenv
- google.golang.org/api

### Requires
- .env
```
TELEGRAM_BOT_TOKEN=<your_telegram_bot_api_token>   
GOOGLE_SHEETS_ID=<google_sheets_id>  
GOOGLE_CREDENTIALS_FILE=<path_to_gc_credentials_json>  
ALLOWED_USER_IDS=<telegram_users_whitelist_separated_by_comma>
```
- [credentials.json](https://developers.google.com/workspace/guides/create-credentials#service-account)
JSON file to authorize service account
