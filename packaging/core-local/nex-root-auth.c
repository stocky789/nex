// SPDX-License-Identifier: AGPL-3.0-only
#define _DEFAULT_SOURCE
#include <security/pam_appl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/resource.h>
#include <unistd.h>

static int converse(int count, const struct pam_message **messages,
                    struct pam_response **response, void *password)
{
    if (count <= 0 || count > PAM_MAX_NUM_MSG) return PAM_CONV_ERR;
    struct pam_response *answers = calloc((size_t)count, sizeof(*answers));
    if (!answers) return PAM_BUF_ERR;
    for (int i = 0; i < count; i++) {
        switch (messages[i]->msg_style) {
        case PAM_PROMPT_ECHO_OFF: answers[i].resp = strdup(password); break;
        case PAM_PROMPT_ECHO_ON: answers[i].resp = strdup("root"); break;
        case PAM_TEXT_INFO: case PAM_ERROR_MSG: continue;
        default: goto fail;
        }
        if (!answers[i].resp) goto fail;
    }
    *response = answers;
    return PAM_SUCCESS;
fail:
    for (int i = 0; i < count; i++) {
        if (answers[i].resp) explicit_bzero(answers[i].resp, strlen(answers[i].resp));
        free(answers[i].resp);
    }
    free(answers);
    return PAM_CONV_ERR;
}

int main(int argc, char **argv)
{
    (void)argv;
    if (argc != 1 || geteuid() != 0) return 1;
    struct rlimit no_core = {0, 0};
    if (setrlimit(RLIMIT_CORE, &no_core)) return 1;
    alarm(15);
    char password[1025] = {0};
    size_t length = fread(password, 1, sizeof(password), stdin);
    if (ferror(stdin) || length == 0 || length > 1024 || memchr(password, '\0', length)) {
        explicit_bzero(password, sizeof(password));
        return 1;
    }
    struct pam_conv conversation = {converse, password};
    pam_handle_t *pam = NULL;
    int result = pam_start("nex-core-local", "root", &conversation, &pam);
    if (result == PAM_SUCCESS) result = pam_authenticate(pam, PAM_DISALLOW_NULL_AUTHTOK);
    if (result == PAM_SUCCESS) result = pam_acct_mgmt(pam, PAM_DISALLOW_NULL_AUTHTOK);
    if (pam) pam_end(pam, result);
    explicit_bzero(password, sizeof(password));
    return result == PAM_SUCCESS ? 0 : 1;
}
