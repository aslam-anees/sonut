#include "audio_io.hpp"
#include "protocol.hpp"

#include <cassert>
#include <sstream>
#include <string>

int main() {
    using namespace atcode::voice;

    Message request;
    std::string error;
    assert(decode(R"({"version":1,"type":"listen.start","utteranceId":"u-1","text":"a\\nquote"})", request, error));
    assert(request.type == "listen.start");
    assert(request.utterance_id == "u-1");

    std::ostringstream encoded;
    Message response;
    response.type = "asr.partial";
    response.utterance_id = "u-1";
    response.text = "hello \"world\"";
    encode(encoded, response);
    assert(encoded.str().find("\\\"world\\\"") != std::string::npos);

    const std::vector<float> input{0.0F, 1.0F, 0.0F, -1.0F};
    const auto output = resample_to_16k(input, 8000);
    assert(output.size() == 8);
    assert(output.front() == 0.0F);

    AudioIO audio;
    std::string device_error;
    assert(!audio.start_input("input-2147483647", device_error));
    assert(!device_error.empty());

    Message invalid;
    assert(!decode(R"({"version":2,"type":"ping"})", invalid, error));
    return 0;
}
