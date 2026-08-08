## Packing it up

The program works. It runs when I open a terminal and type a command.

Which means I have not solved the problem I started with. I used to update NAVs by hand. Now I run a command by hand. The work moved, it did not disappear.

For this to run without me, it has to live somewhere that is always on. Not my laptop, which sleeps and travels and closes. That means a server, and a server needs to be handed the program in a form it can run.

### What a container actually is

I had thought of Docker as something complicated. It is not. A container image is two things:

1. A filesystem
2. A command to run inside it

That is the whole idea. Every line of a Dockerfile is answering one question: **what does this program need present on disk in order to run?**

For a Node program the answer is long. Node itself, npm, the standard library, every package in `node_modules`. For Python it is the interpreter, pip, and the site-packages tree. The program is small and the things it needs are enormous. Images land at 150MB and up.

For Go the answer is almost nothing. The compiler has already pulled every dependency into one file. There is no runtime to install, no interpreter, no library path. The binary is the program.

So the filesystem I need is: my binary. Almost literally that.

### Two stages

Building needs the full Go toolchain, about a gigabyte of compilers and source. Running needs none of it.

Docker lets you say exactly that:

```dockerfile
FROM golang:1.26 AS build
WORKDIR /src
COPY go.mod ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /navsync .

FROM alpine:latest
RUN apk add --no-cache ca-certificates
COPY --from=build /navsync /navsync
ENTRYPOINT ["/navsync"]
```

The second `FROM` starts a fresh, nearly empty image. One line reaches back into the first stage and copies out the binary. Everything else from stage one is thrown away.

It is a workshop and a delivery box. You need every tool in the workshop to build the chair. You do not ship the workshop.

The result:

```
IMAGE            DISK USAGE    CONTENT SIZE
navsync:latest      27.9MB          8.97MB
```

About nine megabytes to download. It took roughly 250MB of toolchain to produce it.

### Two flags I would not have guessed

**`CGO_ENABLED=0`.** By default, Go resolves domain names by calling into the system C library, the same way most programs do. That works fine on my Mac and it means my binary now depends on that C library existing wherever it runs. Alpine does not have it in the form Go expects.

Setting the flag to zero switches Go to its own DNS resolver, written in Go, reading `/etc/resolv.conf` directly. No C dependency. The binary becomes genuinely self contained.

The failure without it is nasty. The binary builds. It starts. Then every network call fails with `no such host` for domains that obviously exist. It looks like a network problem and it is a missing library.

**`ca-certificates`.** An empty base image has no list of trusted certificate authorities. My program talks to two servers over HTTPS, and without a trust store it cannot verify either of them. One line fixes it, and it is the single most common thing people forget.

Both of these are the same lesson. My Mac had been quietly providing things I did not know I was using.

### The line I nearly did not read

The build worked first time. Thirty six seconds. Buried in the output:

```
=> [internal] load build context
=> => transferring context: 25.98MB
```

Twenty six megabytes. My source code is a few kilobytes.

Docker sends the entire working directory to its daemon before the build starts. So it was shipping `navall.txt`, the 1.6MB NAV dump I had saved for offline testing, and my whole `.git` directory with every version of everything I had ever committed. Then `COPY . .` baked all of it into the build stage.

Nothing failed. The image came out the right size, because the second stage only takes the binary. It was just doing a pile of pointless work on every single build, and quietly carrying my git history into a build layer.

The fix is a `.dockerignore`, which works exactly like `.gitignore`:

```
.git
.env
navall.txt
blogs/
*.md
```

Rebuild:

```
=> => transferring context: 3.17kB
```

Twenty six megabytes to three kilobytes. Thirty six seconds to five.

> **Say what should not travel. Nothing else will say it for you.**

### Why go.mod is copied twice

Look at the Dockerfile again. `COPY go.mod ./` comes several lines before `COPY . .`, which would copy `go.mod` anyway. That looks redundant.

Docker caches every step. If nothing a step depends on has changed, it reuses the previous result. My dependencies change once a month. My code changes every few minutes.

Splitting them means the expensive step sits above the volatile one. The second build proved it:

```
=> CACHED [build 3/6] COPY go.mod ./
=> CACHED [build 4/6] RUN go mod download
=> CACHED [stage-1 2/3] RUN apk add --no-cache ca-certificates
=> [build 6/6] RUN CGO_ENABLED=0 go build -o /navsync .    2.7s
```

Everything cached except the compile, which is the only thing that actually changed. Merge those two COPY lines and every code change re-downloads every dependency.

> **Order your build steps from least likely to change to most.**

### It runs

```
2026/08/08 06:13:37 Found 11 pages across 11 ISINs in Notion
2026/08/08 06:13:39 Parsed 17960 ISINs from AMFI.
...
2026/08/08 06:13:50 Done in 12.949s: 11 updated, 0 failed
```

Eleven updated from inside a Linux container. The static binary resolved DNS with no C library and completed TLS to two different servers, so both flags did their jobs.

Though: 12.9 seconds, against 7.9 on my Mac. Not a regression in the code. Docker on macOS runs a Linux virtual machine, so every network packet crosses a boundary that does not exist on a real Linux host. Worth remembering before benchmarking anything in a container locally.

---

## Getting it to run without me

Fly.io takes a container image and runs it on a machine in a datacentre. `fly launch` looks at the repo, spots the Dockerfile, and writes a config file.

Here is what it wrote:

```toml
app = 'go-tools'
primary_region = 'sin'

[build]

[http_service]
  internal_port = 8080
  force_https = true
  auto_stop_machines = 'stop'
  auto_start_machines = true
  min_machines_running = 0

[[vm]]
  memory = '1gb'
```

Every line of that assumes I am deploying a web server.

I am not. My program has no port, no routes, and nothing listening. It wakes up, does eleven writes, and exits.

Had I deployed that as generated, Fly would have sent health checks to port 8080, found nothing answering, decided the machine was broken, and restarted it. Forever. A working program, restarted in a loop, because the config described a different kind of thing.

> **Defaults encode an assumption about what you are building. Read them before you trust them.**

I deleted the whole `[http_service]` block and dropped the memory from 1GB to 256MB, which is still generous for streaming a 5MB file.

### The secrets were already ready

```bash
fly secrets set NOTION_TOKEN=ntn_xxx NOTION_DATA_SOURCE_ID=3b42...
```

Fly encrypts these and injects them as environment variables into the running container.

Not one line of my code changed. `os.Getenv` reads them exactly as it read them from my shell. My program has no idea it is running on Fly, or in a container, or anywhere in particular.

That is the payoff for a decision made days earlier for a completely different reason. I moved the token out of the source because the repo was going public. It turned out to be the thing that made the program portable.

> **Read configuration from the environment and your program stops caring where it runs.**

### The line that made the whole thing work

I deployed, then watched the logs:

```
2026/08/08 06:23:40 Done in 7.888s: 11 updated, 0 failed
INFO Main child exited normally with code: 0
INFO Starting clean up.
machine exited with exit code 0, not restarting
```

Read the last line again.

**Fly read my exit code and decided not to restart the machine.**

Days earlier I had written three lines at the bottom of `main` that felt like tidiness at the time:

```go
if failed > 0 {
	os.Exit(1)
}
```

I wrote them because a scheduler reads exit codes, and back then there was no scheduler. It was a thing I did because it seemed right.

Fly is that scheduler. If my program had exited non-zero, Fly would have treated it as a crash and started it again, and my job would have run in a loop until I noticed. It exited zero, so Fly stopped the machine and left it stopped.

> **The exit code is the interface between your program and everything that runs it.**

Nothing else about my program is visible to Fly. Not the logs, not the counters, not the summary line I was so pleased with. One integer.

### Where does "when" live?

The machine now sits stopped, holding my image, costing nearly nothing. It runs when something starts it. Right now that something is me typing `fly deploy`, which is the same manual trigger I began with, just further away.

The last question is architectural, and it is smaller than it sounds: **should the thing that decides when to run live inside the thing that does the work, or outside it?**

Inside means putting cron in my container and keeping the machine running permanently. My program would sleep for 23 hours and 59 minutes to do eight seconds of work, and I would pay for all of it.

Outside means something else wakes the machine on a schedule. The container stays exactly as it is. The machine stays stopped.

I went outside, with a GitHub Actions workflow that runs `fly machine start`:

```yaml
on:
  schedule:
    - cron: '30 17 * * 1-5'   # 23:00 IST, Mon-Fri
    - cron: '30 2 * * 2-6'    # 08:00 IST, Tue-Sat
  workflow_dispatch:
```

The knowledge of *when* lives in one small file, outside the program, where I can change it without rebuilding anything.

### Two things about that cron

**It is in UTC. Always.** India is UTC plus five and a half hours, so 23:00 at home is 17:30 UTC the same day. Fine.

But 08:00 IST is 02:30 UTC, and 02:30 UTC on Tuesday morning is still Monday evening back home. So the morning run belongs to the *next* day in UTC, which is why one line says `1-5` and the other says `2-6` for what feels like the same working week.

I got this wrong first and only caught it by counting on my fingers. A cron typo does not produce an error. It produces silence at the wrong time.

**Why two runs at all.** Because of something I noticed in the very first week: AMFI's file is never uniformly current. Some funds published today, some yesterday, in a single fetch. Each AMC pushes when it is ready, and stragglers arrive the following morning.

One run at 23:00 catches most of them. The 08:00 run sweeps up the rest.

That is only safe because running the job twice does nothing different from running it once. It reads the current NAV and overwrites a cell. It does not add, append, or accumulate.

> **A job you can safely run twice is a job you can safely schedule.**

I did not design for that. I got it free by choosing to write the NAV rather than append a history row. If I had made the other choice, every retry would have corrupted the data, and every scheduling mechanism I might reach for assumes I did not.

### Done

I pressed Run workflow. GitHub started the machine, Fly ran the container, the container updated Notion, the machine stopped.

My portfolio value now updates twice a day and I am not involved.

The thing I keep turning over is how much of the last two sections was paid for by decisions made much earlier, for unrelated reasons. The token left the source because the repo was going public, and that made the program portable. The exit code was written for a scheduler that did not exist, and it is the only thing Fly actually reads.

Neither felt important at the time. Both were the load bearing part later.