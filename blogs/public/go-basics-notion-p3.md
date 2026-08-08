## Seven of eight seconds spent waiting

The job was deployed and running. 11 writes, 7.888 seconds.

I wanted to know where that went, so I broke it down. AMFI takes about a second. The other seven are eleven requests to Notion, one after another, each around ~600 milliseconds.

But my computer is not busy for those 600 milliseconds. It sends a few hundred bytes to Singapore and then sits there. No calculation, no work. Just waiting for a reply.

So the honest description of my program is that **it spends ninety percent of its life waiting for other computers.**

This fact turns out to be the whole truth. Some work is CPU bound, where the processor is genuinely busy and the only way to speed it up is more cores. Some work is I/O bound, where the processor is blocked on something outside itself. Mine is entirely the second kind.

For I/O bound work you do not need more power. You need the waiting to overlap. 1 requests that each wait ~600ms, waited for at the same time, cost 600ms rather than 6,600ms.

### What Go does that Dart does not (I'm comparing with Dart as this is the language I write most of my code)

I already knew this shape from Flutter. `Future.wait` fires several requests and waits for them together, and it is why an app does not freeze while fetching.

Go looks similar and differs in one way that matters.

Dart runs a single isolate with an event loop. Work interleaves (meaning: to combine different elements so they alternate), but two pieces of code never execute at literally the same instant. Go schedules goroutines across every CPU core, so two really can run simultaneously. And they share memory by default.

Which opens a bug Dart cannot produce.

Our program keeps two counters, `updated` and `failed`. `updated++` looks like one action. It is three:

1. Read the current value
2. Add one
3. Write it back

Two goroutines arrive at once, with the counter at 5:

```
A reads 5
B reads 5          <- before A has written anything
A computes 6, writes 6
B computes 6, writes 6
```

Two successful writes. Counter says 6. One increment has vanished.

In Dart this cannot happen. Between the read and the write, nothing else can run, because Dart only switches tasks at an `await`. There is no `await` inside `updated++`, so it is uninterruptible by construction. Go makes no such promise.

### Deleting the problem instead of guarding it

There are two ways out. Put a lock around every access to the counters, or stop sharing the counters at all.

I picked the second, and the reasoning generalises. A lock is a promise you have to keep in every single place that touches the variable. Miss one and the race is back, with no compiler complaint. Add a second lock and you can deadlock by taking them in different orders.

The other option removes the shared variable entirely. Workers send their outcome down a channel. Only `main` ever touches the counters, and only after every worker has finished.

> **A race needs two goroutines touching the same memory. Remove the sharing and there is nothing left to guard.**

This is what the Go proverb means in practice: 

> **Do not communicate by sharing memory, share memory by communicating.**

### Send the outcome, not a description of it

My first sketch of what a worker sends back had a message field:

```go
type work struct {
	job string
	msg string
}
```

That is wrong in a way worth naming. If the worker formats a message, the worker has already decided how to describe the outcome. `main` then receives a sentence and has to inspect the text to know what happened. You end up checking whether a string contains the word "failed", which is parsing your own prose.

```go
type result struct {
	isin   string
	pageID string
	err    error
}
```

`err == nil` means success. Non-nil means failure, and `main` can print it, count it, or later decide to retry a timeout but not a 400. All of that stays possible because the error arrived intact.

> **Pass values, not formatted strings. Formatting is a decision, and it belongs where the decision is made.**

Same rule as keeping the NAV as a string in the parser, several sections ago. Deciding what something means is not the job of the code that fetches it.

### Three clerks, one queue

The pool itself is small:

```go
jobChannel := make(chan job)
resultChannel := make(chan result, len(jobs))

var waitGroup sync.WaitGroup
for i := 0; i < 3; i++ {
	waitGroup.Add(1)
	go func() {
		defer waitGroup.Done()
		for j := range jobChannel {
			<-limiter.C
			err := updateNAV(token, j.pageID, j.value, j.isoDate)
			resultChannel <- result{isin: j.isin, pageID: j.pageID, err: err}
		}
	}()
}
```

Before I wrote it I assumed the hard part was dividing the work. 11 jobs, three workers, so four and four and three. I went looking for the Go way to slice a list into chunks.

There is no chunking. There is one channel, all three workers read from it, and whoever is free takes the next one.

It is a queue at a counter. Three clerks do not each get a third of the line handed to them at the door. There is one line, and each clerk serves the next person the moment they are free. Divide the line into fixed groups in advance and the clerk who draws three quick customers stands idle while another is still working through a slow one.

That is not a small difference here. My writes ranged from 366ms to 5.124 seconds inside a single run. Any split I picked in advance would have been the wrong one.

> **Do not divide work in advance. Let whoever is free take the next piece.**

The loop body is where my assumption actually dies. `for j := range jobChannel` does not mean "loop over my share". It means "take the next one, and keep taking until there are none left". All three workers run that identical line against that identical channel, and Go guarantees each value goes to exactly one of them.

### The door has to close

`close(jobChannel)` is one line, and it is doing something the code does not look like it needs.

A `range` over a channel does not end when the channel is empty. To a reader, empty and finished are the same thing: nothing here right now. It ends when the channel is closed.

```go
for _, j := range jobs {
	jobChannel <- j
}
close(jobChannel)
```

Leave that last line out and the workers finish the eleventh job, loop round, and block forever waiting for a twelfth that is never coming. `waitGroup.Wait()` then waits on three goroutines that will never return.

The clerks can see the line is empty. They cannot see that the shop has shut. Somebody has to lock the door.

> **A reader cannot tell slow apart from finished. Closing is how you say finished.**

### 11 funds is not 11 writes

`resultChannel` is buffered and `jobChannel` is not. That asymmetry is the part I would have got wrong.

Start with the size, because it is not eleven.

```go
resultChannel := make(chan result, len(jobs))
```

`buildJobs` walks a `map[string][]string`, ISIN to page IDs, and emits one job per page. The same fund held under two investors is two rows in Notion, two page IDs, two writes. That map was a decision from several sections back, made because a plain `map[string]string` silently kept only the last page and dropped the rest. It is still earning its place here. The buffer is sized off the work, not off the funds.

Then the reason it needs a buffer at all, which is the ordering back in `main`:

```go
waitGroup.Wait()
close(resultChannel)

for r := range resultChannel {
```

`main` does not read a single result until every worker has exited. So the buffer is not smoothing out a burst. It has to hold every result at once, because nothing drains it until the last worker is gone. `len(jobs)` is not a generous guess. It is the smallest number that works, and it cannot overflow, because every job produces exactly one result.

Take the buffer away and this happens. `main` is blocked sending job four into an unbuffered `jobChannel`. All three workers are blocked sending their first result into an unbuffered `resultChannel`. Four goroutines, each waiting on one of the others.

I wanted to see that rather than argue about it, so I rebuilt the same pool with the buffer removed and ran it:

```
fatal error: all goroutines are asleep: deadlock!

goroutine 1 [chan send]:
main.main()
	/.../main.go:40 +0x194

goroutine 35 [chan send]:
main.main.func1()
	/.../main.go:34 +0x90
created by main.main in goroutine 1
	/.../main.go:30 +0xfc
```

Line 40 is `jobChannel <- j`. Line 34 is `resultChannel <- result{...}`. The runtime named both sides of the standoff, and the exact lines, before the program had written anything.

There is a second way to get this wrong, and it is the same two lines in the other order. Close `resultChannel` before `waitGroup.Wait()` and a worker sends into a closed channel. That is `panic: send on closed channel`. Loud again.

### The bugs that shout are the cheap ones

That is worth sitting with, because it is the opposite of everything else in this post.

A parser that skipped nine funds exited zero. A struct tag missing a quote compiled. A benchmark told me concurrency had made the program slower and I believed it. None of them said a word.

Get the concurrency wrong and Go halts the program, prints every goroutine, and points at the line. The part I was most nervous about writing is the part that refuses to fail quietly.

> **The bugs that crash are the cheap ones. Save your fear for the ones that exit zero.**

There is one line in that worker I have not explained.

```go
<-limiter.C
```

Three clerks, and before serving anyone, each one waits for a bell. I put it there because Notion allows about three requests a second, and I had three workers, and I thought those were the same sentence.

### The knob I reached for was the wrong one

Notion allows roughly three requests a second. My instinct was to use three workers and call that the rate limit.

It is not. Those are two different things.

Worker count controls how many requests are in flight. Rate controls how often a new one starts.

Three workers, each request taking 600ms, gives about 5 requests a second. Over the limit. Now suppose Notion slows down and each request takes two seconds. The same three workers now produce 1.5 a second. Under the limit, and slower than I need to be.

So worker count gives a rate that **drifts with the server's response time**. When Notion is struggling, I would hit it hardest right as it was recovering. That is exactly backwards.

The fix is to control the rate directly, with one ticker shared by every worker:

```go
limiter := time.NewTicker(334 * time.Millisecond)
defer limiter.Stop()

// inside each worker, before the request:
<-limiter.C
```

> **Rate limit the requests, not the workers. Worker count is a concurrency limit. A ticker is a rate limit. They are different knobs.**

One ticker, shared. Give each worker its own and you have tripled the rate while believing you limited it.

### It was slower

I ran it. 9.721 seconds.

The sequential version had been 7.888.

I had added goroutines, channels, a wait group and a rate limiter, and made the program twenty percent slower. That is not what the arithmetic said should happen.

So before changing anything, I measured properly. A timestamp around each request, and three runs of the same code:

```
Run 1: mostly 400-500ms per request, total 5.5s
Run 2: mostly 1.3-2.0s per request, total 8.2s
Run 3: mixed,                        total 7.0s
```

Same code. Same 11 requests. **Fifty percent swing between runs.**

Which meant my original conclusion was worthless. 7.888 against 9.721 is one sample of each, and both numbers sit comfortably inside the range I had just measured. I had not made the program slower. I had rolled a dice twice.

> **A benchmark with fifty percent run to run variance cannot measure a twenty percent improvement.**

Nothing errored. Nothing warned. I made a confident wrong inference from real data, which is the same failure this whole project keeps producing in different costumes.

The honest numbers, once I took medians over several runs: about **5.3 seconds** concurrent against **7.9 seconds** sequential. A real improvement, just not one a single run could have told me.

### Something odd about empty cells

While testing I cleared the `nav` and `nav_date` columns, and that run felt slow.

The obvious response is that it was noise. I had just proved I could not tell a real difference from a bad afternoon.

So I alternated. Clear the cells, run. Run again with them populated. Clear, run. Run again. Four runs, two conditions, interleaved so that drifting network conditions land on both.

The totals were unconvincing: 7.9 and 6.8 for empty, 5.6 and 5.0 for populated. A forty percent gap, which is inside the noise I had already measured.

The individual request times were not:

```
Empty:      1.465s  2.131s  1.62s  1.985s  1.252s  1.784s
Populated:   448ms   464ms   478ms   460ms   535ms   439ms
```

Three times the latency, at the median, consistently, alternating exactly with the condition.

> **The signal was in the distribution, not the total.**

The totals hid it because one bad request drags a whole run. In one earlier run a single write took 5.124 seconds when the same fund had taken 366ms a minute before. That one number accounted for most of the run total.

> **When one sample dominates the total, the total is not measuring what you think.**

Writing a value into an empty property costs Notion more than overwriting one that already has a value. Creating rather than updating. It changes nothing about how I run the job, since after the first day every cell is always populated. But I would not have found it by looking at run totals, and I would not have believed it without alternating.

---

## The schedule that never fired

Two things left. Run the tests, and watch it fire on its own.

### No test files

```
$ go test ./... -race
?       go-tools        [no test files]
```

My test was gone. I had written it, run it, watched it catch a real parser bug, and then never committed it. Somewhere in the refactoring it left the disk too.

Which means `go test ./...` had been reporting success for days. Not failing. Not warning. Just quietly finding nothing to run and exiting zero.

> **A test that is not in the repository is not a test.**

I rewrote it and committed it this time.

Worth being precise about what `-race` proved once it passed, though. The race detector only flags races it actually observes, and my test never calls the worker pool. So it confirmed the parser works. It did not confirm the pool is safe.

The evidence for the pool is structural rather than empirical. The counters are only touched by `main`, after every worker has finished. There is no shared memory for two goroutines to fight over. That is why I chose channels over a lock, and it is a better guarantee than a test passing once.

### 20:30 came and went

I set the cron for 20:30 IST and nothing happened.

My first assumption was that I had got the UTC conversion wrong, since I had already made that mistake once. I checked. `00 15` UTC is 20:30 IST. Correct.

The Actions tab told the real story. Every NAV sync run said "Manually run by". And the workflow file itself had last been changed at 18:06.

I had committed the schedule after the time had already passed. The slot was gone before GitHub knew the schedule existed. Nothing was broken.

Two things I learned in the process of not needing them:

GitHub only runs scheduled workflows from the default branch. And a schedule set for a round number like `:00` or `:30` is competing with everyone else's, so those are the ones dropped first under load. An odd minute gets served more reliably.

> **A cron typo produces silence, not an error. Test it with a time five minutes away, not tomorrow.**

Which is what I did. Set it ten minutes out, push, wait, and watch for a run that does not say "Manually run by".

### The last silent failure

Looking back at all of it, the thing that keeps repeating is not really about Go.

A parser that skipped nine funds and printed six. A scanner that stopped early and reported success. A struct tag missing a quote that matched nothing and compiled. A hundred rows arriving where there were more. A benchmark that told me the opposite of the truth. A test suite with no tests in it. A cron that fired into a time that had already passed.

None of them crashed. Every one of them exited zero.

> The hard part was not writing the code. It was learning that code which runs is not code which works.