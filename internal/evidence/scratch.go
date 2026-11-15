package evidence

// ScratchDirName is the Test step's scratch directory inside a run's evidence
// directory: the driver scripts, throwaway fixtures, configs and data the
// evidence turn needs that are not evidence. One per run keeps two concurrent
// runs from sharing a fixed path under /tmp, which a real evidence turn used
// when no place was named for its scratch files.
//
// It lives in the evidence directory so the daemon's evidence reaper bounds it
// with the run, and every consumer reads this one name: the Test step creates
// it and names it in its prompt, publication excludes it (Request.ExcludeDirs),
// the PR body neither embeds nor names a file in it (isTestScratchPath in
// internal/pipeline/steps), and run cleanup removes it when the agent left it empty, so an unused
// scratch directory never keeps an otherwise empty run directory alive.
const ScratchDirName = "scratch"
