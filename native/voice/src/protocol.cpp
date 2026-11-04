#include "protocol.hpp"

#include <iomanip>
#include <ostream>
#include <sstream>

namespace atcode::voice {
namespace {

std::string field(const std::string & line, const std::string & name) {
    const std::string needle = "\"" + name + "\":";
    const auto start = line.find(needle);
    if (start == std::string::npos) return {};
    auto value = start + needle.size();
    while (value < line.size() && (line[value] == ' ' || line[value] == '\t')) ++value;
    if (value >= line.size()) return {};
    if (line[value] == '"') {
        ++value;
        std::string result;
        bool escaped = false;
        for (; value < line.size(); ++value) {
            const char c = line[value];
            if (escaped) {
                switch (c) {
                case 'n': result.push_back('\n'); break;
                case 'r': result.push_back('\r'); break;
                case 't': result.push_back('\t'); break;
                default: result.push_back(c); break;
                }
                escaped = false;
            } else if (c == '\\') {
                escaped = true;
            } else if (c == '"') {
                break;
            } else {
                result.push_back(c);
            }
        }
        return result;
    }
    const auto end = line.find_first_of(",}", value);
    return line.substr(value, end == std::string::npos ? std::string::npos : end - value);
}

int integer_field(const std::string & line, const std::string & name, int fallback) {
    try { return std::stoi(field(line, name)); } catch (...) { return fallback; }
}

double number_field(const std::string & line, const std::string & name, double fallback) {
    try { return std::stod(field(line, name)); } catch (...) { return fallback; }
}

}  // namespace

std::string json_escape(const std::string & value) {
    std::ostringstream out;
    for (const unsigned char c : value) {
        switch (c) {
        case '"': out << "\\\""; break;
        case '\\': out << "\\\\"; break;
        case '\n': out << "\\n"; break;
        case '\r': out << "\\r"; break;
        case '\t': out << "\\t"; break;
        default:
            if (c < 0x20) out << "\\u" << std::hex << std::setw(4) << std::setfill('0') << static_cast<int>(c) << std::dec;
            else out << static_cast<char>(c);
        }
    }
    return out.str();
}

bool decode(const std::string & line, Message & message, std::string & error) {
    if (line.size() > 1024 * 1024) { error = "protocol line exceeds 1 MiB"; return false; }
    message = Message{};
    message.version = integer_field(line, "version", 0);
    message.type = field(line, "type");
    message.request_id = field(line, "requestId");
    message.utterance_id = field(line, "utteranceId");
    message.text = field(line, "text");
    message.language = field(line, "language");
    message.voice = field(line, "voice");
    message.speed = number_field(line, "speed", 0.0);
    message.silence_ms = integer_field(line, "silenceMs", 0);
    message.profile = field(line, "profile");
    message.input_device = field(line, "inputDevice");
    message.output_device = field(line, "outputDevice");
    message.state = field(line, "state");
    message.error = field(line, "error");
    if (message.version != kProtocolVersion) { error = "unsupported protocol version"; return false; }
    if (message.type.empty()) { error = "protocol message has no type"; return false; }
    return true;
}

void encode(std::ostream & out, const Message & message) {
    out << "{\"version\":" << kProtocolVersion << ",\"type\":\"" << json_escape(message.type) << "\"";
    if (!message.request_id.empty()) out << ",\"requestId\":\"" << json_escape(message.request_id) << "\"";
    if (!message.utterance_id.empty()) out << ",\"utteranceId\":\"" << json_escape(message.utterance_id) << "\"";
    if (!message.text.empty()) out << ",\"text\":\"" << json_escape(message.text) << "\"";
    if (!message.language.empty()) out << ",\"language\":\"" << json_escape(message.language) << "\"";
    if (!message.voice.empty()) out << ",\"voice\":\"" << json_escape(message.voice) << "\"";
    if (message.speed != 0.0) out << ",\"speed\":" << message.speed;
    if (message.silence_ms != 0) out << ",\"silenceMs\":" << message.silence_ms;
    if (!message.state.empty()) out << ",\"state\":\"" << json_escape(message.state) << "\"";
    if (!message.error.empty()) out << ",\"error\":\"" << json_escape(message.error) << "\"";
    if (!message.devices_json.empty()) out << ",\"devices\":" << message.devices_json;
    out << "}\n";
    out.flush();
}

}  // namespace atcode::voice
