package tools

import "strings"

func RecoveryHints(toolName, errorClass, errorMessage string) []string {
	if moves := recoveryHintsForTool(toolName, errorClass, errorMessage); len(moves) > 0 {
		return moves
	}

	switch errorClass {
	case "path_is_directory":
		return []string{
			"Use `bash` to inspect the directory contents instead of trying to read it as a file.",
			"Choose a concrete file path, preferably an absolute path rooted at the injected workspace or project root.",
		}
	case "path_not_found":
		return []string{
			"Check whether the path is relative to the agent workspace and switch to an absolute path if needed.",
			"Use `bash` to verify the file exists before calling the file tool again.",
		}
	case "permission_denied":
		return []string{
			"Choose a tool or path that is allowed in the current runtime policy.",
			"Explain the permission boundary clearly and continue with other verifiable work when possible.",
		}
	case "validation_error":
		return []string{
			"Read the exact parameter error and fix the arguments before retrying.",
			"Switch tools if another tool matches the task more directly.",
		}
	case "timeout":
		return []string{
			"Reduce the scope, split the work into smaller calls, or retry with a shorter-running command.",
			"Use `bash` plus a short script when the task is repetitive or easier to control programmatically.",
		}
	case "network_error", "transient_provider_error":
		return []string{
			"Retry after a short wait because the failure looks transient.",
			"If transient retries keep failing, switch methods or continue with the parts that can still be verified locally.",
		}
	case "loop_detected":
		return []string{
			"Stop repeating the same tool call unchanged; change arguments, switch tools, or report the blockage explicitly.",
			"Use the most recent tool error or result to decide on a different next step.",
		}
	default:
		moves := []string{
			"Inspect the exact error and adjust the next step instead of repeating the same call unchanged.",
			"Switch tools or use `bash` when it gives you more control over the task.",
		}
		if strings.TrimSpace(toolName) == "read_file" {
			moves = append(moves, "If the path might be a directory, use `bash` first to inspect it.")
		}
		return moves
	}
}

func recoveryHintsForTool(toolName, errorClass, errorMessage string) []string {
	normalizedTool := strings.ToLower(strings.TrimSpace(toolName))
	switch normalizedTool {
	case "write_file":
		return recoveryHintsForWriteFile(errorClass, errorMessage)
	case "edit_file":
		return recoveryHintsForEditFile(errorClass, errorMessage)
	case "read_file":
		return recoveryHintsForReadFile(errorClass)
	case "bash", "python", "python3":
		return recoveryHintsForProcessTool(normalizedTool, errorClass)
	case "browser":
		return recoveryHintsForBrowser(errorClass, errorMessage)
	case "web_search", "web_fetch":
		return recoveryHintsForWebTool(normalizedTool, errorClass)
	case "callagent":
		return recoveryHintsForCallAgent(errorClass)
	default:
		return nil
	}
}

func recoveryHintsForWriteFile(errorClass, errorMessage string) []string {
	switch errorClass {
	case "validation_error":
		if IsMalformedToolInputError(errorMessage) {
			return []string{
				"The `write_file` arguments were not valid JSON, so the file write did not start; avoid repeating the same malformed call.",
				"If the content is large or quote-heavy, retry with smaller `write_file` chunks: first `mode=overwrite`, then `mode=append` with `expected_offset`.",
				"For existing-file edits, use `write_file` with `mode=patch` and a Codex-style patch instead of rewriting the whole file.",
			}
		}
		return []string{
			"Fix the `write_file` JSON shape first: include `path` plus `content`, `patch`, or `old_string`/`new_string`.",
			"When content contains many newlines, quotes, or backslashes, split it into smaller `write_file` append chunks and use `expected_offset`.",
			"After writing, read or validate the target file before continuing.",
		}
	case "path_not_found":
		return []string{
			"Use an absolute path under the injected workspace or project root; `write_file` will create missing parent directories for allowed paths.",
			"If the base directory is ambiguous, inspect it with `bash` before writing.",
		}
	case "permission_denied":
		return []string{
			"Choose a writable path inside the current workspace or another path allowed by runtime policy.",
			"Do not retry the same forbidden path; explain the boundary if no allowed write target exists.",
		}
	case "timeout":
		return []string{
			"Reduce the write size by splitting it into `write_file` overwrite/append chunks with `expected_offset`.",
			"Verify the final file size and content after the write completes.",
		}
	}
	return nil
}

func recoveryHintsForEditFile(errorClass, errorMessage string) []string {
	if errorClass != "validation_error" {
		return nil
	}
	errLower := strings.ToLower(strings.TrimSpace(errorMessage))
	switch {
	case strings.Contains(errLower, "old_string not found"):
		return []string{
			"Read the target file around the intended location and retry with an exact `old_string` copied from the current file.",
			"If the target changed substantially, use `bash`/`python` for a controlled rewrite or transformation instead of guessing the replacement.",
		}
	case strings.Contains(errLower, "must be unique"):
		return []string{
			"Make `old_string` more specific by including surrounding lines so it matches exactly one occurrence.",
			"Use `bash` to inspect matching locations before retrying the edit.",
		}
	case IsMalformedToolInputError(errorMessage):
		return []string{
			"The `edit_file` arguments were malformed JSON; rebuild the call with `path`, `old_string`, and `new_string` as valid strings.",
			"For large replacements, prefer a short `python` script that reads, replaces, writes, and verifies the file.",
		}
	default:
		return []string{
			"Read the exact parameter error and rebuild the edit with `path`, `old_string`, and `new_string`.",
			"Use `read_file` or `bash` to inspect the current file before retrying.",
		}
	}
}

func recoveryHintsForReadFile(errorClass string) []string {
	switch errorClass {
	case "path_is_directory":
		return []string{
			"`read_file` reads file contents only; use `bash` to list or inspect directories.",
			"Then choose a concrete file path, preferably an absolute path under the injected workspace or project root.",
		}
	case "path_not_found":
		return []string{
			"Check the path against the injected workspace/project root and switch to an absolute path if needed.",
			"Use `bash` to list nearby directories or find the intended file before retrying.",
		}
	}
	return nil
}

func recoveryHintsForProcessTool(toolName, errorClass string) []string {
	switch errorClass {
	case "timeout":
		return []string{
			"Split the command into a shorter inspection or verification step, or pass an appropriate timeout when the tool supports it.",
			"If starting a long-running service, run it in the background and add an explicit readiness check before browser/search verification.",
		}
	case "permission_denied":
		return []string{
			"Use commands and paths allowed by the runtime policy.",
			"Switch to file tools or another available tool if shell execution is blocked.",
		}
	case "validation_error":
		if toolName == "python3" {
			toolName = "python"
		}
		return []string{
			"Fix the `" + toolName + "` arguments before retrying; keep the command/script small enough to be valid JSON.",
			"For large generated content, write a script or heredoc in smaller pieces and verify the output file.",
		}
	}
	return nil
}

func recoveryHintsForBrowser(errorClass, errorMessage string) []string {
	errLower := strings.ToLower(strings.TrimSpace(errorMessage))
	switch errorClass {
	case "network_error":
		moves := []string{
			"If navigating to localhost, confirm the service is still running and listening on the expected host/port before retrying.",
			"Start or restart the server with `bash`, wait for a health check or open port, then call `browser` again.",
		}
		if strings.Contains(errLower, "err_connection_refused") || strings.Contains(errLower, "connection refused") {
			moves = append(moves, "A connection-refused browser error usually means the page server was never started, exited early, or was cleaned up before navigation.")
		}
		return moves
	case "timeout":
		return []string{
			"Wait for the app/server to become ready before navigating, or use a narrower browser action after the page loads.",
			"Reduce page work where possible and verify the target URL with `bash`/`curl` before another browser call.",
		}
	case "validation_error":
		return []string{
			"Fix the browser action arguments, including `action`, `url`, `selector`, or `script` as required for that action.",
			"If the failure depends on page state, use `get_text`, `screenshot`, or `evaluate` to inspect the current page before retrying.",
		}
	}
	return nil
}

func recoveryHintsForWebTool(toolName, errorClass string) []string {
	switch errorClass {
	case "timeout", "network_error", "transient_provider_error":
		if toolName == "web_search" {
			return []string{
				"Retry the search with a shorter, more specific query; transient network/search failures are often recoverable.",
				"If search keeps failing, use known URLs with `web_fetch` or continue from local documentation and cite the limitation.",
			}
		}
		return []string{
			"Retry the fetch after a short wait because the failure looks transient.",
			"If the URL keeps failing, search for an alternate official/source URL or continue with locally verifiable evidence.",
		}
	case "validation_error":
		return []string{
			"Fix the web tool arguments before retrying; use a concrete query or URL.",
			"Switch between `web_search` and `web_fetch` depending on whether you need discovery or a known page.",
		}
	}
	return nil
}

func recoveryHintsForCallAgent(errorClass string) []string {
	switch errorClass {
	case "timeout":
		return []string{
			"Narrow the delegated task or split it into smaller independent subtasks.",
			"Use the partial evidence already available instead of repeatedly waiting on the same stalled agent call.",
		}
	case "validation_error":
		return []string{
			"Fix the agent call arguments, especially the target agent name and concrete task text.",
			"Keep the delegated task self-contained and bounded so the agent can complete it.",
		}
	}
	return nil
}

func IsMalformedToolInputError(errorMessage string) bool {
	errLower := strings.ToLower(strings.TrimSpace(errorMessage))
	return strings.Contains(errLower, "unexpected end of json input") ||
		strings.Contains(errLower, "invalid character") ||
		strings.Contains(errLower, "cannot unmarshal") ||
		strings.Contains(errLower, "json:") ||
		strings.Contains(errLower, "malformed")
}
