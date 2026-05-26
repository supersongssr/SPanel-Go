I will list the files in the directory to find where the source code for this website or server is located so that I can investigate these bugs.
I will list our current permission grants to see what parts of the filesystem or external resources we can access.
I will run a command to check the current working directory and find files related to the project in `/root` or elsewhere to locate the project codebase.
I will search for any web project directories on the system.
I will list the directories and files in `/root` up to a depth of 3 to find where the project source code is stored.
I will search the filesystem for the file `announcements.html` or similar files to pinpoint where the frontend or full-stack web project is located.
I will check the directory contents of `/root/git/SPanel-Go` and `/root/git/SPanel-bun` to see the structure of these projects and how they relate to the target site.
I will view `TODO.md` and `api.log` in `/root/git/SPanel-Go` to understand what changes or active logs might explain the current errors.
I will view the `api.log` file in `/root/git/SPanel-Go` to see what server requests or errors are logged.
I will search the `/etc/nginx` configuration directory to see how `test-spanel-go.freessr.bid` is proxying requests to the backend server.
I will view `/etc/nginx/sites-available/test-spanel-go` to see how the reverse proxy is configured for our target host.
I will view `/root/git/SPanel-Go/public/admin/announcements.html` to see what endpoint it is trying to request and why it might be failing.
I will read the rest of the `/root/git/SPanel-Go/public/admin/announcements.html` file to see the Alpine.js code that fetches the announcements and handles errors.
I will view `/root/git/SPanel-Go/public/admin/announcements.html` from line 151 onwards to see the Alpine.js methods.
I will read `/root/git/SPanel-Go/public/assets/js/api.js` to see how the AJAX requests are configured, in order to identify why they fail to parse JSON and why they might return non-JSON responses.
I will search the Go project directory to list the controller/handler source files where these endpoints are handled in the backend.
I will view `/root/git/SPanel-Go/config.yaml` to see the database and server configuration.
I will read `/root/git/SPanel-Go/internal/app/handler/public.go` to examine the public endpoints such as `/auth/captcha` and `/auth/login` to see what responses they generate and how they might fail.
I will view `/root/git/SPanel-Go/public/auth/login.html` to see how it queries the captcha endpoint and what error handling it utilizes.
I will view `/root/git/SPanel-Go/public/auth/login.html` starting from line 151 to understand how it handles the response from the `/auth/captcha` API.
I will test the local backend API running on port 8080 by making requests with `curl` to see if they return valid JSON or if they are throwing errors/HTML.
I will check which processes are running to see if the Go backend is actually running, and what ports are open.
I will list all enabled Nginx sites to see if there is another configuration that routes the request.
I will view `/etc/nginx/sites-available/test-spanel-bun` to see how the Bun site configuration differs from the Go site configuration.
I will read `/root/git/SPanel-Go/run.sh` to see how the Go backend is supposed to be started.
I will run `./run.sh status` in the `/root/git/SPanel-Go` directory to see if the script thinks the service is running, and if it printed any errors in the stale PID scenario.
I will run a command to tail the last 50 lines of `api.log` to check for any crash or error messages that might have been logged since the server started.
I will manually execute the `spanel-api` binary in the foreground to capture any immediate panics, database connection failures, or exit logs.
I will view the stdout/stderr log of the running backend process to see if it starts successfully or if it crashes immediately.
I will verify if port 8080 is now open and responding to API requests correctly since running the binary.
I will test `/api/v1/auth/captcha` locally on the server.
I will list the Nginx log directory to see if there are any recorded proxy errors or access patterns that would help explain why the Go backend server crashed or was stopped.
I will check the last 100 lines of `/var/log/nginx/error.log` to see if Nginx recorded the connection failures to port 8080 or other errors.
I will list the active background tasks to see if our manually started `spanel-api` is still running.
I will kill the manual foreground task `task-58` and start the backend service properly using `./run.sh` in the background so it survives independent of our session.
I will now run `./run.sh restart` in the `/root/git/SPanel-Go` directory to clean up any stale PID file and launch the backend service correctly in the background as a system service.
I will verify the status of the service using the `./run.sh status` command to make sure it remains running.
I will read the last 50 lines of `api.log` to check the error message explaining why the service stopped immediately after being started by `run.sh`.
I will check if the PID 1839318 is still active, or if it died immediately after starting.
I will view `/root/git/SPanel-Go/cmd/api/main.go` to see the initialization logic and if there are any conditions or goroutines that would cause it to terminate or crash silently.
I will check the `/root/nodeMonitor.sh` file to see if there is an active monitor script that is killing or restarting processes automatically.
I will list the active cron jobs to see if there is any scheduled task that kills or modifies processes.
I will check if `systemctl` is available or if we are in a containerized environment where a persistent terminal is needed to keep background processes alive.
I will check if there is an existing systemd service for SPanel.
I will search all systemd unit files to see if a systemd service is defined for the Go backend.
I will locate and view the `/etc/systemd/system/spanel-bun.service` unit file to see how it is structured, which will help us create a matching systemd service for SPanel-Go.
I will view `/etc/systemd/system/spanel-bun.service` to understand the service configuration.
I will list the files in `/root/git/SPanel-Go/plans` to see if there are any deployment plans or service configurations already prepared for Go.
I will view `/root/git/SPanel-Go/plans/2026-05-26_bugfix-newfun.md` to see what previous bugfixes or changes were planned or executed.
I will view `/root/git/SPanel-Go/plans/2026-05-26_bugfix-newfun.done.md` to see the details of the changes made previously.
I will create the systemd unit file `/etc/systemd/system/spanel-go.service` to manage SPanel-Go. This ensures it runs as a persistent service, automatically restarts if it crashes, and starts up on system boot.
I will reload the systemd daemon, enable the `spanel-go` service, start it, and check its running status.
I will check the status of the `spanel-go` service again after a short delay to ensure it stays active and doesn't exit.
I will curl the local API endpoint now to verify that the systemd service is active and responding correctly.
I will test the external URL of the API using `curl` to ensure the public Nginx reverse proxy routes traffic correctly.
I will create a walkthrough markdown artifact outlining the root cause of these errors and detailing the exact steps taken to fix them permanently.
I have investigated and resolved the issues causing the page errors and captcha loading failures.

### Root Cause
All of the reported errors—both the `JSON.parse` unexpected character error on the admin pages and the network/captcha loading error on the authentication pages—stemmed from a single root cause: **the Go backend service (`spanel-api`) on port `8080` was not running**. 

Because the backend was offline, the Nginx reverse proxy responded to all `/api/` fetch requests with its default **502 Bad Gateway** HTML page. When the frontend's AJAX core attempted to parse these HTML responses as JSON, the parser threw the `unexpected character at line 1 column 1` syntax error.

### Solution
1. **Configured a systemd Service**: Instead of launching the backend inside an interactive terminal session that would get cleaned up when the session disconnected, I created a standard systemd service file at `/etc/systemd/system/spanel-go.service`.
2. **Enabled and Started the Service**:
   - Reloaded the systemd configuration.
   - Configured the service to start automatically on system boot.
   - Started the `spanel-go` service.
3. **Verification**: Checked both the local listener on port `8080` and the public HTTPS URL. The endpoints now respond correctly with the expected JSON payload instead of an HTML error.

A detailed summary, configuration files, and verification outputs are documented in the [walkthrough.md](file:///root/.gemini/antigravity-cli/brain/04d22dc7-3e64-42ac-9852-432612247134/walkthrough.md) artifact. 

All pages on `https://test-spanel-go.freessr.bid` should now load and function correctly. Please let me know if you encounter any further issues!