# Brakelight

**Brakelight** is a self-contained Go service that watches directories for new media files, queues them for HandBrakeCLI conversion, and exposes a small web UI for monitoring and reordering jobs.

## Features

- Watches multiple directories, each mapped to a HandBrake preset.
- Accepts HandBrake-readable containers: `.mkv`, `.mp4`, `.m4v`, `.avi`, `.mov`, `.ts`, `.m2ts`, `.mts`, `.flv`, `.wmv`, `.asf`, `.webm`, `.mpg`, `.mpeg`, `.mpe`, `.vob`, `.3gp`, `.3g2`, `.ogv`, `.ogm`, `.divx`.
- Persists queue state in SQLite.
- Processes one job at a time.
- Reorder pending jobs via the web UI.
- Retry failed jobs, or cancel pending ones.
- Keeps job history and HandBrake output for debugging.
- Embeds sidecar subtitles: if `a.srt` exists next to `a.mkv`, it is added to the output as an English subtitle track and marked as default.
- Review watches (`review = true`): files are probed with ffprobe when queued and wait in the queue until you pick their audio/subtitle tracks in the web UI, per file or in bulk for all checked files. The picker pre-selects the original-language audio track when the file marks one (e.g. an English original with an Italian default dub keeps only English). Selections are stored as rules (`all`, `none`, `original`, `lang:eng`, `tracks:1,3`) and resolved per file at encode time; if a rule matches nothing, all tracks are kept. Jobs from review watches are not encoded before a selection is made.
- Pause/resume the service from the UI; pausing stops new scans and conversions while letting the active encode finish. The state persists across restarts.

## Build

```bash
go build -o brakelight ./cmd/brakelight
```

## Configuration

Create an HCL config file, e.g. `brakelight.hcl`. See [HCL syntax](https://developer.hashicorp.com/packer/docs/templates/hcl_templates/syntax) for reference.

```hcl
config {
  output_dir        = "/media/encoded"
  user_presets      = "/Users/bob/Library/Containers/fr.handbrake.HandBrake/Data/Library/Application Support/HandBrake/UserPresets.json"
  handbrake_cli     = "/opt/homebrew/bin/HandBrakeCLI"

  # Optional: ffprobe binary used to inspect tracks of review-watch files.
  # Default: resolved from PATH.
  # ffprobe_cli      = "/opt/homebrew/bin/ffprobe"
  log_file          = "/Users/bob/Library/Logs/brakelight.log"
  scan_interval     = "30s"
  partial_extension = ".partial"
  max_attempts      = 3
  listen_addr       = ":8080"

  # Optional: default preset for watch blocks without an explicit preset.
  default_preset    = "Standard"

  # Optional: custom location for the queue database.
  db_file           = "/data/brakelight/myqueue.db"
}

# A minimal setup for watching a directory.
watch "General" {
  path   = "/media/queue/general"
  preset = Default"
}

# A review watch: queued files are probed and wait for a per-file (or bulk)
# audio/subtitle track selection in the web UI before encoding.
watch "Review" {
  path   = "/media/queue/review"
  review = true
}

watch "Animated" {
  path   = "/media/queue/animated"
  preset = "Animated"

  # Optional: override the default output_dir for this watch directory.
  output_dir = "/media/encoded/animated"

  # Optional: shell commands run before each encode starts.
  # {output} = full path, {output_path} = directory, {output_file} = basename.
  # NOTE: the output file does not exist yet at this point.
  # Failures are logged and shown in the UI, but do not fail the job unless
  # fail_on_pre_command_error is set.
  pre_commands = [
    "logger 'Starting: {output_file}'",
  ]

  # Optional: fail the job (skip the encode) when a pre-command fails.
  # Default: false.
  fail_on_pre_command_error = true

  # Optional: shell commands run after each encode completes.
  # Run with /bin/sh -c; failures are logged and shown in the UI, but do not
  # fail the job. Same placeholders as pre_commands.
  post_commands = [
    "logger 'Encoded: {output_file}'",
  ]
}
```

The queue database (`queue.db`) tracks pending, processing, and completed
jobs. By default it is stored in the per-user application data directory:

- macOS: `~/Library/Application Support/brakelight/queue.db`
- Linux: `$XDG_CONFIG_HOME/brakelight/queue.db` (or `~/.config/brakelight/queue.db` when unset)
- Windows: `%AppData%\brakelight\queue.db`

Only macOS is currently supported. Use `db_file` in the config block to set a
custom path and/or file name; relative paths resolve against the working
directory.

## Run

```bash
./brakelight -config brakelight.hcl
```

Open `http://localhost:8080` in a browser.
