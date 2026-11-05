// SPDX-License-Identifier: MIT
// Pure ABI/parser fixtures, not a live Falco event or containerd observation.
#include "plugin.cpp"
#include <cassert>
#include <iostream>
#include <fstream>

static std::string cid(64, 'a'), generation(64, 'b');
static unsigned released = 0;
static std::string snapshot(int64_t now, const std::vector<std::string> &ids, std::string box = "worker") {
    std::string raw = "yoloctl-audit-meta-v1\t" + std::to_string(now) + "\t" + std::to_string(ids.size()) + "\t" + generation + "\n";
    for (auto &id : ids) raw += id + "\t" + box + "\twork\tnone\tcodex\tconfined\tfalse\n";
    return raw;
}
static ss_plugin_table_t *get_table(ss_plugin_owner_t *, const char *name, ss_plugin_state_type type) {
    assert(std::string(name) == "threads" && type == SS_PLUGIN_ST_INT64);
    return reinterpret_cast<ss_plugin_table_t *>(1);
}
static ss_plugin_table_field_t *get_field(ss_plugin_table_t *, const char *name, ss_plugin_state_type type) {
    assert(std::string(name) == "container_id" && type == SS_PLUGIN_ST_STRING);
    return reinterpret_cast<ss_plugin_table_field_t *>(2);
}
static ss_plugin_table_entry_t *get_entry(ss_plugin_table_t *, const ss_plugin_state_data *key) {
    assert(key->s64 == 42);
    return reinterpret_cast<ss_plugin_table_entry_t *>(3);
}
static ss_plugin_rc read_field(ss_plugin_table_t *, ss_plugin_table_entry_t *, const ss_plugin_table_field_t *, ss_plugin_state_data *out) {
    out->str = cid.c_str(); return SS_PLUGIN_SUCCESS;
}
static void release_entry(ss_plugin_table_t *, ss_plugin_table_entry_t *) { released++; }
int main(int argc, char **argv) {
    assert(argc == 2);
    std::ifstream fixture(argv[1]);
    std::string golden((std::istreambuf_iterator<char>(fixture)),std::istreambuf_iterator<char>());
    auto protocol = yolo::Snapshot::parse(golden,100);
    assert(protocol.find(cid,100) && protocol.find(cid,100)->values[0] == "worker");
    auto now = seconds_now();
    auto raw = snapshot(now, {cid});
    auto data = yolo::Snapshot::parse(raw, now);
    assert(data.find(cid, now) && data.find(cid.substr(0, 12), now));
    assert(!data.find("host", now) && !data.find(std::string(64, 'c'), now));
    assert(!data.find(cid, now + 16));
    assert(yolo::Snapshot::parse(raw, now + 16).records.empty());
    assert(yolo::Snapshot::parse(raw, now - 3).records.empty());
    auto nextGeneration = raw;nextGeneration.replace(nextGeneration.find(generation),generation.size(),std::string(64,'c'));
    assert(yolo::Snapshot::parse(nextGeneration,now).generation==std::string(64,'c'));
    assert(yolo::Snapshot::parse(raw.substr(0, raw.size()-1), now).records.empty());
    assert(yolo::Snapshot::parse(snapshot(now, {cid,cid}), now).records.empty());
    assert(yolo::Snapshot::parse(snapshot(now, {cid}, "../escape"), now).records.empty());
    assert(yolo::Snapshot::parse(std::string(yolo::max_bytes + 1, '\n'), now).records.empty());
    auto second = cid; second.back() = 'b';
    auto ambiguous = yolo::Snapshot::parse(snapshot(now, {cid,second}), now);
    assert(!ambiguous.find(cid.substr(0,12), now));
    assert(ambiguous.find(cid, now) && ambiguous.find(second, now));
    auto recreated = yolo::Snapshot::parse(snapshot(now, {std::string(64,'c')}), now);
    assert(!recreated.find(cid,now)); // Reusing the box name never adopts the old CID.
    auto fields_api = ss_plugin_table_fields_vtable_ext{}; fields_api.get_table_field = get_field;
    auto tables = ss_plugin_init_tables_input{}; tables.get_table = get_table; tables.fields_ext = &fields_api;
    std::string path = "/private/metadata.tsv";
    auto init = ss_plugin_init_input{}; init.config = path.c_str(); init.tables = &tables;
    ss_plugin_rc rc{}; auto opaque = plugin_init(&init, &rc); assert(rc == SS_PLUGIN_SUCCESS && opaque);
    auto &state = *reinterpret_cast<State *>(opaque);
    auto reader = ss_plugin_table_reader_vtable_ext{}; reader.get_table_entry = get_entry; reader.read_entry_field = read_field; reader.release_table_entry = release_entry;
    auto extract = ss_plugin_field_extract_input{}; extract.table_reader_ext = &reader;
    std::array<ss_plugin_extract_field,7> fields{};
    for (size_t i=0;i<fields.size();++i) fields[i].field_id = uint32_t(i);
    extract.num_fields = fields.size(); extract.fields = fields.data();
    ss_plugin_event event{}; event.tid=42; event.ts=uint64_t(now)*1000000000; event.len=sizeof(event);
    ss_plugin_event_input input{&event,1,"syscall"};
    for (int scenario=0;scenario<5;++scenario) {
        state.cache = data; state.next_refresh = std::chrono::steady_clock::now() + std::chrono::hours(1);
        cid = std::string(64,'a');
        if (scenario==1) cid=cid.substr(0,12);
        if (scenario==2) state.cache=ambiguous, cid=cid.substr(0,12);
        if (scenario==3) state.cache=recreated;
        if (scenario==4) state.cache.generated=now-16;
        assert(plugin_extract_fields(opaque,&input,&extract)==SS_PLUGIN_SUCCESS);
        bool available=scenario<2;
        assert(fields[6].res_len==1 && bool(*fields[6].res.boolean)==available);
        assert(released==unsigned(scenario+1));
        if (available) {
            assert(fields[0].res_len==1 && std::string(*fields[0].res.str)=="worker");
            assert(fields[5].res_len==1 && !*fields[5].res.boolean);
        } else for (size_t i=0;i<6;++i) assert(fields[i].res_len==0);
    }
    state.cache=data; cid=std::string(64,'a'); state.cache.records[0].values[5]="true";
    state.next_refresh=std::chrono::steady_clock::now()+std::chrono::hours(1);
    assert(plugin_extract_fields(opaque,&input,&extract)==SS_PLUGIN_SUCCESS && *fields[5].res.boolean && *fields[6].res.boolean);
    assert(released==6);
    state.next_refresh={}; // Missing file invalidates even a previously valid cache.
    state.cache=data; cid=std::string(64,'a');
    assert(plugin_extract_fields(opaque,&input,&extract)==SS_PLUGIN_SUCCESS && !*fields[6].res.boolean);
    plugin_destroy(opaque);
    std::cout << "PASS: bounded snapshot, known/full/prefix/missing/stale/ambiguous/recreated identity, ABI types/lifetime/entry release; no live capture\n";
}
