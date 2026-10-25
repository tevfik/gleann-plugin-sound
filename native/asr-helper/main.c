// gleann-asr-helper: a small sidecar around transcribe.cpp (https://github.com/handy-computer/transcribe.cpp).
//
// gleann-plugin-sound already links whisper.cpp, and two statically linked copies
// of ggml cannot live in one binary, so transcribe.cpp runs as a separate process.
// That also isolates crashes and lets the model be loaded once and shared.
//
//   gleann-asr-helper stream  <model.gguf> <language> [att_right]   cache-aware streaming (Nemotron)
//   gleann-asr-helper offline <model.gguf> <language>               whole-utterance transcription (Qwen3-ASR)
//
// Requests arrive on stdin as 1 opcode byte; replies are one JSON line each on stdout.
//
//   'A' u32 n, n x int16 LE   stream: feed PCM        -> {"text":"<utterance so far>","committed":"..","tentative":"..","ms":N}
//   'F'                       stream: finalize+reset  -> {"text":"<final utterance>","committed":"..","tentative":"","ms":N,"final":true}
//   'R'                       stream: discard utterance and start a new one -> {"ok":true}
//   'T' u32 n, n x int16 LE   offline: transcribe     -> {"text":".."}
//   'Q'                       quit
//
// PCM is 16 kHz mono. Errors are reported as {"error":".."}; the process keeps running.

#include "transcribe.h"
#include "transcribe/parakeet.h"

#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static void json_str(const char * s) {
    putchar('"');
    for (; s && *s; ++s) {
        unsigned char c = (unsigned char) *s;
        if (c == '"' || c == '\\') { putchar('\\'); putchar(c); }
        else if (c == '\n') fputs("\\n", stdout);
        else if (c == '\r') fputs("\\r", stdout);
        else if (c == '\t') fputs("\\t", stdout);
        else if (c < 0x20) printf("\\u%04x", c);
        else putchar(c);
    }
    putchar('"');
}

static void reply_error(const char * msg) {
    fputs("{\"error\":", stdout); json_str(msg); fputs("}\n", stdout); fflush(stdout);
}

static int read_exact(void * buf, size_t n) { return fread(buf, 1, n, stdin) == n; }

static float * read_pcm(uint32_t n) {
    int16_t * raw = (int16_t *) malloc((size_t) n * sizeof(int16_t));
    float *   out = (float *) malloc((size_t) n * sizeof(float));
    if (!raw || !out || !read_exact(raw, (size_t) n * sizeof(int16_t))) { free(raw); free(out); return NULL; }
    for (uint32_t i = 0; i < n; ++i) out[i] = (float) raw[i] / 32768.0f;
    free(raw);
    return out;
}

static void reply_stream(struct transcribe_session * s, long long ms, int final_) {
    // "text" is the cumulative raw hypothesis; "committed"/"tentative" are the
    // library's own append-only split of it (committed never changes once sent).
    const char * text = transcribe_full_text(s);
    struct transcribe_stream_text t;
    transcribe_stream_text_init(&t);
    const char * committed = "", * tentative = "";
    if (transcribe_stream_get_text(s, &t) == TRANSCRIBE_OK) {
        if (t.committed_text) committed = t.committed_text;
        if (t.tentative_text) tentative = t.tentative_text;
    }
    fputs("{\"text\":", stdout); json_str(text ? text : "");
    fputs(",\"committed\":", stdout); json_str(committed);
    fputs(",\"tentative\":", stdout); json_str(tentative);
    printf(",\"ms\":%lld%s}\n", ms, final_ ? ",\"final\":true" : "");
    fflush(stdout);
}

static int begin_stream(struct transcribe_session * s, const char * lang, int att_right) {
    struct transcribe_run_params rp;
    transcribe_run_params_init(&rp);
    rp.timestamps = TRANSCRIBE_TIMESTAMPS_AUTO;
    if (lang && *lang && strcmp(lang, "auto") != 0 && strcmp(lang, "none") != 0) {
        rp.language = lang;
    }
    struct transcribe_stream_params sp;
    transcribe_stream_params_init(&sp);
    struct transcribe_parakeet_stream_ext ext;
    transcribe_parakeet_stream_ext_init(&ext);
    if (att_right >= 0) { ext.att_context_right = att_right; sp.family = &ext.ext; }
    return transcribe_stream_begin(s, &rp, &sp);
}

int main(int argc, char ** argv) {
    if (argc < 4) { fprintf(stderr, "usage: %s stream|offline model.gguf language [att_right]\n", argv[0]); return 2; }
    const int stream_mode = strcmp(argv[1], "stream") == 0;
    const char * lang = argv[3];
    const int att_right = argc > 4 ? atoi(argv[4]) : -1;

    struct transcribe_session * s = NULL;
    transcribe_status st = transcribe_open(argv[2], NULL, NULL, &s);
    if (st != TRANSCRIBE_OK) { fprintf(stderr, "open %s: %s\n", argv[2], transcribe_status_string(st)); return 1; }
    if (stream_mode && (st = begin_stream(s, lang, att_right)) != TRANSCRIBE_OK) {
        fprintf(stderr, "stream_begin: %s\n", transcribe_status_string(st)); return 1;
    }
    printf("{\"ready\":true,\"mode\":\"%s\"}\n", stream_mode ? "stream" : "offline");
    fflush(stdout);

    long long fed_ms = 0;
    int op;
    while ((op = getchar()) != EOF && op != 'Q') {
        if (op == 'A' || op == 'T') {
            uint32_t n = 0;
            if (!read_exact(&n, 4)) break;
            float * pcm = read_pcm(n);
            if (!pcm) { reply_error("short read"); break; }
            if (op == 'A' && stream_mode) {
                struct transcribe_stream_update upd;
                transcribe_stream_update_init(&upd);
                st = transcribe_stream_feed(s, pcm, (int) n, &upd);
                if (st != TRANSCRIBE_OK) reply_error(transcribe_status_string(st));
                else { fed_ms += (long long) n * 1000 / 16000; reply_stream(s, fed_ms, 0); }
            } else if (op == 'T' && !stream_mode) {
                struct transcribe_run_params rp;
                transcribe_run_params_init(&rp);
                rp.timestamps = TRANSCRIBE_TIMESTAMPS_AUTO;
                if (lang && *lang && strcmp(lang, "auto") != 0 && strcmp(lang, "none") != 0) {
                    rp.language = lang;
                }
                st = transcribe_run(s, pcm, (int) n, &rp);
                if (st != TRANSCRIBE_OK) reply_error(transcribe_status_string(st));
                else { fputs("{\"text\":", stdout); json_str(transcribe_full_text(s)); fputs("}\n", stdout); fflush(stdout); }
            } else reply_error("opcode does not match mode");
            free(pcm);
        } else if (op == 'F' && stream_mode) {
            struct transcribe_stream_update fin;
            transcribe_stream_update_init(&fin);
            st = transcribe_stream_finalize(s, &fin);
            if (st != TRANSCRIBE_OK) reply_error(transcribe_status_string(st));
            else reply_stream(s, fed_ms, 1);
            transcribe_stream_reset(s);
            if ((st = begin_stream(s, lang, att_right)) != TRANSCRIBE_OK) { reply_error(transcribe_status_string(st)); break; }
            fed_ms = 0;
        } else if (op == 'R' && stream_mode) {
            transcribe_stream_reset(s);
            st = begin_stream(s, lang, att_right);
            fed_ms = 0;
            if (st != TRANSCRIBE_OK) reply_error(transcribe_status_string(st));
            else { fputs("{\"ok\":true}\n", stdout); fflush(stdout); }
        } else if (op != '\n') {
            reply_error("unknown opcode");
        }
    }
    transcribe_session_free(s);
    return 0;
}
