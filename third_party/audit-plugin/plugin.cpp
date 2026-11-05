// SPDX-License-Identifier: MIT
#include <plugin/plugin_api.h>
#include "snapshot.hpp"
#include <chrono>
#include <cerrno>
#include <cstring>
#include <fcntl.h>
#include <memory>
#include <sys/stat.h>
#include <unistd.h>

namespace {
int64_t seconds_now() {
    return std::chrono::duration_cast<std::chrono::seconds>(std::chrono::system_clock::now().time_since_epoch()).count();
}
struct State {
    std::string path;
    ss_plugin_table_t *threads = nullptr;
    ss_plugin_table_field_t *container = nullptr;
    yolo::Snapshot cache;
    std::chrono::steady_clock::time_point next_refresh{};
    std::array<std::string, 5> strings;
    std::array<const char *, 5> pointers{};
    ss_plugin_bool available = 0, network = 0;
    void refresh(int64_t now) {
        auto tick = std::chrono::steady_clock::now();
        if (tick < next_refresh) return;
        next_refresh = tick + std::chrono::seconds(1);
        cache = {};
        int fd = open(path.c_str(), O_RDONLY | O_CLOEXEC | O_NOFOLLOW | O_NONBLOCK);
        if (fd < 0) return;
        struct OwnedFD { int value; ~OwnedFD() { close(value); } } owned{fd};
        struct stat st{};
        if (fstat(fd, &st) || !S_ISREG(st.st_mode) || st.st_uid != 0 || (st.st_mode & 0777) != 0600 || st.st_nlink != 1 ||
            st.st_size <= 0 || st.st_size > int64_t(yolo::max_bytes)) return;
        std::string raw(size_t(st.st_size) + 1, '\0');
        size_t size = 0;
        while (size < raw.size()) {
            auto n = read(fd, &raw[size], raw.size() - size);
            if (n < 0) { if (errno == EINTR) continue; return; }
            if (!n) break;
            size += size_t(n);
        }
        if (size != size_t(st.st_size)) return;
        raw.resize(size);
        cache = yolo::Snapshot::parse(raw, now);
    }
};
}
#define EXPORT extern "C" __attribute__((visibility("default")))
EXPORT const char *plugin_get_required_api_version() { return "3.12.0"; }
EXPORT const char *plugin_get_version() { return "1.0.0"; }
EXPORT const char *plugin_get_name() { return "yolo"; }
EXPORT const char *plugin_get_description() { return "Bounded nonsecret yoloctl box metadata; not process-confinement proof"; }
EXPORT const char *plugin_get_contact() { return "https://github.com/leogr/yoloctl"; }
EXPORT const char *plugin_get_init_schema(ss_plugin_schema_type *type) { *type = SS_PLUGIN_SCHEMA_NONE; return nullptr; }
EXPORT const char *plugin_get_last_error(ss_plugin_t *state) { (void)state; return "yolo metadata unavailable; need the pinned container plugin and a fresh private snapshot"; }
EXPORT const char *plugin_get_extract_event_sources() { return "[\"syscall\"]"; }
EXPORT const char *plugin_get_fields() {
    return R"([{"name":"yolo.box","type":"string","desc":"Recorded box name"},{"name":"yolo.space","type":"string","desc":"Recorded space name"},{"name":"yolo.account","type":"string","desc":"Nonsecret account name or none"},{"name":"yolo.agent","type":"string","desc":"Recorded agent name or none"},{"name":"yolo.runtime_mode","type":"string","desc":"Recorded launch mode, not per-process confinement proof"},{"name":"yolo.network_unconfined","type":"bool","desc":"Recorded network launch mode"},{"name":"yolo.metadata_available","type":"bool","desc":"Fresh unambiguous supported metadata is available"}])";
}
EXPORT ss_plugin_t *plugin_init(const ss_plugin_init_input *input, ss_plugin_rc *rc) {
    *rc = SS_PLUGIN_FAILURE;
    try {
        auto state = std::make_unique<State>();
        if (!input || !input->config || !input->tables || !input->tables->get_table || !input->tables->fields_ext ||
            !input->tables->fields_ext->get_table_field) return nullptr;
        state->path = input->config;
        // Configuration is the fixed private controller file path, not a JSON document.
        auto slash = state->path.rfind('/');
        if (state->path.empty() || state->path[0] != '/' || slash == std::string::npos) return nullptr;
        auto leaf = state->path.substr(slash + 1);
        if (leaf != "metadata.tsv") return nullptr;
        state->threads = input->tables->get_table(input->owner, "threads", SS_PLUGIN_ST_INT64);
        if (state->threads) state->container = input->tables->fields_ext->get_table_field(state->threads, "container_id", SS_PLUGIN_ST_STRING);
        if (!state->threads || !state->container) { return reinterpret_cast<ss_plugin_t *>(state.release()); }
        *rc = SS_PLUGIN_SUCCESS;
        return reinterpret_cast<ss_plugin_t *>(state.release());
    } catch (...) { return nullptr; }
}
EXPORT void plugin_destroy(ss_plugin_t *state) { delete reinterpret_cast<State *>(state); }
EXPORT ss_plugin_rc plugin_extract_fields(ss_plugin_t *opaque, const ss_plugin_event_input *event, const ss_plugin_field_extract_input *input) {
    if (!opaque || !event || !event->evt || !input || !input->fields || input->num_fields > 64) return SS_PLUGIN_FAILURE;
    auto &state = *reinterpret_cast<State *>(opaque);
    try {
        state.available = 0; state.network = 0;
        auto now = seconds_now(); state.refresh(now);
        auto reader = input->table_reader_ext;
        if (event->evtsrc && std::strcmp(event->evtsrc, "syscall") == 0 && reader && reader->get_table_entry && reader->read_entry_field && reader->release_table_entry) {
            ss_plugin_state_data key{}; key.s64 = int64_t(event->evt->tid);
            auto entry = reader->get_table_entry(state.threads, &key);
            if (entry) {
                ss_plugin_state_data value{};
                const yolo::Record *record = nullptr;
                if (reader->read_entry_field(state.threads, entry, state.container, &value) == SS_PLUGIN_SUCCESS && value.str) {
                    // Framework-owned strings are copied while the entry is held.
                    auto size = strnlen(value.str, 65);
                    if (size == 12 || size == 64) record = state.cache.find(std::string_view(value.str, size), now);
                }
                reader->release_table_entry(state.threads, entry);
                if (record) {
                    for (size_t n = 0; n < state.strings.size(); ++n) { state.strings[n] = record->values[n]; state.pointers[n] = state.strings[n].c_str(); }
                    state.available = 1; state.network = record->values[5] == "true";
                }
            }
        }
        for (uint32_t n = 0; n < input->num_fields; ++n) {
            auto &field = input->fields[n]; field.res_len = 0;
            if (field.field_id > 6 || field.arg_present || field.flist) return SS_PLUGIN_FAILURE;
            if (field.field_id == 6) { field.res.boolean = &state.available; field.res_len = 1; }
            else if (state.available) {
                field.res_len = 1;
                if (field.field_id == 5) field.res.boolean = &state.network;
                else field.res.str = &state.pointers[field.field_id];
            }
        }
        return SS_PLUGIN_SUCCESS;
    } catch (...) { state.cache = {}; return SS_PLUGIN_FAILURE; }
}
