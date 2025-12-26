<div align="center">
  <h1>goclaimer</h1>
  <p><em>Realtime discord vanity sniper</em></p>
</div>

<p align="center">
<img src="https://img.shields.io/github/stars/00nx/discord-vanity-sniper?style=social" alt="Stars"> &nbsp;
<img src="https://img.shields.io/github/forks/00nx/discord-vanity-sniper?style=social" alt="Forks"> &nbsp;
<img src="https://img.shields.io/github/license/00nx/discord-vanity-sniper" alt="License"> &nbsp;
<img src="https://img.shields.io/github/last-commit/00nx/discord-vanity-sniper" alt="Last Commit">
</p>


## Features

- Real-time detection via Discord WebSocket (no polling)
- Ultra-fast claiming with `fasthttp` and parallel requests
- Automatic MFA token generation / bypass
- Multi-guild support (load target guilds from `guilds.txt`)
- Detailed Discord webhook notifications (claim speed, success/failure)
- Auto-reload `config.json` and `guilds.txt` on changes
- Optional auto-leave original server after successful claim

> [!CAUTION]
> This tool uses automation that may violate Discord's Terms of Service (self-bot behavior).  
**Use at your own risk**.. account bans are possible.  
Provided for **educational purposes only**.


**Installation:**

1. Clone the repository
   ```bash
   git clone https://github.com/00nx/discord-vanity-sniper.git
   cd discord-vanity-sniper
   ```
2. Build
   ```bash
   go build -o sniper.exe main.go
   ```
3. Update ```config.json``` with your details

4. add your server ids on ``guilds.txt`` line by line ( must have 14 boosts )
5. Run the file
   ```bash
   sniper.exe
   ```





