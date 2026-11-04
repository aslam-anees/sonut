#pragma once

#include "engine/framework/core/backend.h"
#include "engine/framework/runtime/model.h"
#include "engine/framework/runtime/registry.h"
#include "engine/framework/runtime/session.h"

#include <memory>
#include <string>
#include <vector>

namespace atcode::voice {

class ASRSession {
public:
    bool load(const std::string & model_path, const std::string & vad_model_path, engine::core::BackendType backend, std::string & error);
    bool start(const std::string & language, std::string & error);
    bool process(const std::vector<float> & samples, std::string & partial, std::string & language, std::string & error);
    bool finish(std::string & text, std::string & language, std::string & error);
    void cancel();

private:
    std::unique_ptr<engine::runtime::ILoadedVoiceModel> model_;
    std::unique_ptr<engine::runtime::IVoiceTaskSession> session_base_;
    engine::runtime::IStreamingVoiceTaskSession * session_ = nullptr;
    bool started_ = false;
};

}  // namespace atcode::voice
