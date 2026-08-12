//go:build linux && cgo && pam
// +build linux,cgo,pam

package auth

/*
#cgo LDFLAGS: -lpam
#include <security/pam_appl.h>
#include <stdlib.h>
#include <string.h>

struct rt_conversation_data {
	const char *username;
	const char *password;
};

static void rt_free_responses(struct pam_response *responses, int count) {
	if (responses == NULL) return;
	for (int i = 0; i < count; i++) {
		if (responses[i].resp != NULL) {
			memset(responses[i].resp, 0, strlen(responses[i].resp));
			free(responses[i].resp);
		}
	}
	free(responses);
}

static int rt_conversation(int count, const struct pam_message **messages,
		struct pam_response **response, void *data_ptr) {
	if (count <= 0 || messages == NULL || response == NULL || data_ptr == NULL) {
		return PAM_CONV_ERR;
	}
	struct rt_conversation_data *data = (struct rt_conversation_data *)data_ptr;
	struct pam_response *answers = calloc((size_t)count, sizeof(struct pam_response));
	if (answers == NULL) return PAM_BUF_ERR;

	for (int i = 0; i < count; i++) {
		if (messages[i] == NULL) {
			rt_free_responses(answers, count);
			return PAM_CONV_ERR;
		}
		const char *answer = NULL;
		switch (messages[i]->msg_style) {
		case PAM_PROMPT_ECHO_OFF:
			answer = data->password;
			break;
		case PAM_PROMPT_ECHO_ON:
			answer = data->username;
			break;
		case PAM_ERROR_MSG:
		case PAM_TEXT_INFO:
			answer = "";
			break;
		default:
			rt_free_responses(answers, count);
			return PAM_CONV_ERR;
		}
		answers[i].resp = strdup(answer == NULL ? "" : answer);
		if (answers[i].resp == NULL) {
			rt_free_responses(answers, count);
			return PAM_BUF_ERR;
		}
		answers[i].resp_retcode = 0;
	}
	*response = answers;
	return PAM_SUCCESS;
}

static int rt_pam_authenticate(const char *service, const char *username,
		const char *password) {
	pam_handle_t *handle = NULL;
	struct rt_conversation_data data = { username, password };
	struct pam_conv conversation = { rt_conversation, &data };
	int result = pam_start(service, username, &conversation, &handle);
	if (result == PAM_SUCCESS) {
		result = pam_authenticate(handle, PAM_DISALLOW_NULL_AUTHTOK);
	}
	if (result == PAM_SUCCESS) {
		result = pam_acct_mgmt(handle, PAM_DISALLOW_NULL_AUTHTOK);
	}
	if (handle != NULL) {
		int end_result = pam_end(handle, result);
		if (result == PAM_SUCCESS && end_result != PAM_SUCCESS) result = end_result;
	}
	return result;
}
*/
import "C"

import (
	"context"
	"unsafe"
)

type pamAuthenticator struct{ service string }

func newPAMAuthenticator(service string) Authenticator {
	return &pamAuthenticator{service: service}
}

func (p *pamAuthenticator) Authenticate(ctx context.Context, username, password string) error {
	if err := ctx.Err(); err != nil {
		return ErrUnavailable
	}
	cService := C.CString(p.service)
	cUsername := C.CString(username)
	cPassword := C.CString(password)
	defer C.free(unsafe.Pointer(cService))
	defer C.free(unsafe.Pointer(cUsername))
	defer func() {
		// Reduce the time the plaintext remains in C-managed memory.
		length := len(password)
		if length > 0 {
			C.memset(unsafe.Pointer(cPassword), 0, C.size_t(length))
		}
		C.free(unsafe.Pointer(cPassword))
	}()

	result := C.rt_pam_authenticate(cService, cUsername, cPassword)
	if result != C.PAM_SUCCESS {
		return ErrInvalidCredentials
	}
	return nil
}
