# Brakelight

**Brakelight** is a self-contained Go service that watches directories for new media files, queues them for HandBrakeCLI conversion, and exposes a small web UI for monitoring and reordering jobs.

## Features

- Watches multiple directories, each mapped to a HandBrake preset.
- Persists queue state in SQLite.
- Processes one job at a time.
- Reorder pending jobs via the web UI.
- Retry failed jobs, or cancel pending ones.
- Keeps job history and HandBrake output for debugging.
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

watch "Animated" {
  path   = "/media/queue/animated"
  preset = "Animated"

  # Optional: override the default output_dir for this watch directory.
  output_dir = "/media/encoded/animated"

  # Optional: shell commands run before each encode starts.
  # {output} = full path, {output_path} = directory, {output_file} = basename.
  # NOTE: the output file does not exist yet at this point.
  pre_commands = [
    "logger 'Starting: {output_file}'",
  ]

  # Optional: shell commands run after each encode completes.
  # Run with /bin/sh -c; failures are logged but do not fail the job.
  # Same placeholders and behavior as pre_commands.
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
