#pragma once

#include "engine/framework/core/backend.h"
#include "engine/framework/runtime/model.h"
#include "engine/framework/runtime/registry.h"
#include "engine/framework/runtime/session.h"

#include <memory>
#include <functional>
#include <string>
#include <vector>

namespace atcode::voice {

class TTSSession {
public:
    bool load(const std::string & model_path, engine::core::BackendType backend, std::string & error);
    bool supported_language(const std::string & language) const;
    bool synthesize(const std::string & text, const std::string & language, const std::string & voice, double speed,
                    const std::function<bool(const std::vector<float> &, int)> & on_audio, std::string & error);

private:
    std::unique_ptr<engine::runtime::ILoadedVoiceModel> model_;
    std::unique_ptr<engine::runtime::IVoiceTaskSession> session_base_;
    engine::runtime::IStreamingVoiceTaskSession * session_ = nullptr;
};

}  // namespace atcode::voice
