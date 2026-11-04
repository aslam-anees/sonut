cmake_minimum_required(VERSION 3.20)

project(atcode_voice LANGUAGES C CXX)

include(FetchContent)
include(CTest)

set(CMAKE_CXX_STANDARD 17)
set(CMAKE_CXX_STANDARD_REQUIRED ON)
set(CMAKE_CXX_EXTENSIONS OFF)
set(CMAKE_RUNTIME_OUTPUT_DIRECTORY "${CMAKE_BINARY_DIR}/bin")

set(ATCODE_AUDIO_CPP_REVISION "238ab6a9e321c17de8e120559f57efeedaeb1345" CACHE STRING "Pinned audio.cpp revision")
set(ATCODE_VOICE_ENABLE_CUDA OFF CACHE BOOL "Build the optional CUDA backend")
set(ATCODE_VOICE_ENABLE_VULKAN OFF CACHE BOOL "Build the optional Vulkan backend")
if(APPLE AND CMAKE_SYSTEM_PROCESSOR MATCHES "^(arm64|aarch64)$")
  set(ATCODE_VOICE_ENABLE_METAL ON CACHE BOOL "Build the Apple Metal backend")
else()
  set(ATCODE_VOICE_ENABLE_METAL OFF CACHE BOOL "Build the Apple Metal backend")
endif()

# audio.cpp is fetched by immutable commit, and deployment mode embeds the
# model-spec catalog in engine_runtime. The reference checkout under ref/ is
# never consulted by this build.
FetchContent_Declare(
  audio_cpp
  GIT_REPOSITORY https://github.com/0xShug0/audio.cpp.git
  GIT_TAG ${ATCODE_AUDIO_CPP_REVISION}
  GIT_SHALLOW FALSE
  GIT_SUBMODULES_RECURSE TRUE
)
FetchContent_GetProperties(audio_cpp)
if(NOT audio_cpp_POPULATED)
  FetchContent_Populate(audio_cpp)
endif()

set(AUDIOCPP_MODEL_SET custom CACHE STRING "audio.cpp model selection" FORCE)
set(AUDIOCPP_MODELS "qwen3_asr,supertonic" CACHE STRING "atcode voice model selection" FORCE)
set(AUDIOCPP_DEPLOYMENT_BUILD ON CACHE BOOL "Embed model specs" FORCE)
set(ENGINE_BUILD_EXAMPLES OFF CACHE BOOL "Do not build audio.cpp examples" FORCE)
set(ENGINE_BUILD_TESTS OFF CACHE BOOL "Do not build audio.cpp tests" FORCE)
set(ENGINE_BUILD_WARMBENCH OFF CACHE BOOL "Do not build audio.cpp warm benches" FORCE)
set(ENGINE_ENABLE_CUDA ${ATCODE_VOICE_ENABLE_CUDA} CACHE BOOL "audio.cpp CUDA" FORCE)
set(ENGINE_ENABLE_VULKAN ${ATCODE_VOICE_ENABLE_VULKAN} CACHE BOOL "audio.cpp Vulkan" FORCE)
set(ENGINE_ENABLE_METAL ${ATCODE_VOICE_ENABLE_METAL} CACHE BOOL "audio.cpp Metal" FORCE)
set(ENGINE_ENABLE_NATIVE_CPU ON CACHE BOOL "Use native CPU kernels" FORCE)
set(ENGINE_ENABLE_LLAMAFILE OFF CACHE BOOL "Do not build an unused llamafile backend" FORCE)
add_subdirectory("${audio_cpp_SOURCE_DIR}" "${audio_cpp_BINARY_DIR}" EXCLUDE_FROM_ALL)

# miniaudio is a single-header library. Pin both the source version and bytes;
# no system package manager or Python runtime is required.
FetchContent_Declare(
  miniaudio
  URL https://raw.githubusercontent.com/mackron/miniaudio/0.11.22/miniaudio.h
  URL_HASH SHA256=9019743287e443c55e5737a7297f38e5e358561701d6db2d905afb114390c410
  DOWNLOAD_NO_EXTRACT TRUE
  DOWNLOAD_NAME miniaudio.h
)
FetchContent_Populate(miniaudio)

add_executable(atcode-voice
  src/main.cpp
  src/protocol.cpp
  src/audio_io.cpp
  src/asr.cpp
  src/tts.cpp
)
target_include_directories(atcode-voice PRIVATE
  src
  "${miniaudio_SOURCE_DIR}"
)
target_link_libraries(atcode-voice PRIVATE engine_runtime ggml)
if(APPLE)
  target_compile_definitions(atcode-voice PRIVATE ATCODE_VOICE_PLATFORM_MACOS=1)
elseif(WIN32)
  target_compile_definitions(atcode-voice PRIVATE ATCODE_VOICE_PLATFORM_WINDOWS=1)
else()
  target_compile_definitions(atcode-voice PRIVATE ATCODE_VOICE_PLATFORM_LINUX=1)
endif()

install(TARGETS atcode-voice RUNTIME DESTINATION bin)

if(BUILD_TESTING)
  add_executable(atcode-voice-native-tests
    tests/native_test.cpp
    src/protocol.cpp
    src/audio_io.cpp
  )
  target_include_directories(atcode-voice-native-tests PRIVATE src "${miniaudio_SOURCE_DIR}")
  add_test(NAME atcode-voice-native-tests COMMAND atcode-voice-native-tests)
endif()
