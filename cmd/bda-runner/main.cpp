#define _CRT_SECURE_NO_WARNINGS
#define WIN32_LEAN_AND_MEAN
#include <windows.h>
#include <dshow.h>
#include <initguid.h>
#include <ks.h>
#include <ksmedia.h>
#include <bdaiface.h>
#include <bdamedia.h>
#include <tuner.h>
#include <io.h>
#include <fcntl.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

// Explicit GUID definitions for maximum portability across MinGW and MSVC
#ifndef KSCATEGORY_BDA_NETWORK_TUNER
DEFINE_GUID(KSCATEGORY_BDA_NETWORK_TUNER,
    0x71985f48, 0x1ca1, 0x11d3, 0x9c, 0xc8, 0x00, 0xc0, 0x4f, 0x79, 0x71, 0xe0);
#endif

#ifndef KSCATEGORY_BDA_RECEIVER_COMPONENT
DEFINE_GUID(KSCATEGORY_BDA_RECEIVER_COMPONENT,
    0xfd0a5cd3, 0x41ae, 0x4a5e, 0xac, 0x34, 0x5d, 0x14, 0x8b, 0xd3, 0x88, 0x8d);
#endif

#ifndef CLSID_DVBTNetworkProvider
DEFINE_GUID(CLSID_DVBTNetworkProvider,
    0x216de000, 0x8a7e, 0x464a, 0xa2, 0xe4, 0xbe, 0x33, 0xd1, 0xed, 0x6b, 0xfa);
#endif

#ifndef CLSID_DVBTuningSpace
DEFINE_GUID(CLSID_DVBTuningSpace,
    0xc6b14b32, 0x76aa, 0x4a86, 0xa7, 0xac, 0x5f, 0xa7, 0x96, 0x07, 0x8c, 0x2d);
#endif

#ifndef CLSID_DVBTLocator
DEFINE_GUID(CLSID_DVBTLocator,
    0x9cd64701, 0xbdf3, 0x4d14, 0x8e, 0x07, 0x0e, 0x5c, 0x13, 0x8c, 0x8d, 0x66);
#endif

class CTSSink;

// Minimal pin enumerator for CTSSink
class CEnumPins : public IEnumPins {
private:
    LONG m_refCount;
    CTSSink* m_pSink;
    int m_pos;
public:
    CEnumPins(CTSSink* pSink);
    virtual ~CEnumPins();
    STDMETHODIMP QueryInterface(REFIID riid, void** ppv);
    STDMETHODIMP_(ULONG) AddRef();
    STDMETHODIMP_(ULONG) Release();
    STDMETHODIMP Next(ULONG cPins, IPin** ppPins, ULONG* pcFetched);
    STDMETHODIMP Skip(ULONG cPins);
    STDMETHODIMP Reset();
    STDMETHODIMP Clone(IEnumPins** ppEnum);
};

// Custom TS Sink Filter that pipes MPEG-TS buffers to standard output
class CTSSink : public IBaseFilter, public IPin, public IMemInputPin {
private:
    LONG m_refCount;
    IPin* m_pConnectedPin;
    AM_MEDIA_TYPE m_mediaType;
    FILTER_STATE m_state;
    IFilterGraph* m_pGraph;
    HANDLE m_hStdOut;
public:
    CTSSink() : m_refCount(1), m_pConnectedPin(NULL), m_state(State_Stopped), m_pGraph(NULL) {
        ZeroMemory(&m_mediaType, sizeof(m_mediaType));
        m_hStdOut = GetStdHandle(STD_OUTPUT_HANDLE);
    }
    virtual ~CTSSink() {
        if (m_pConnectedPin) m_pConnectedPin->Release();
    }

    // IUnknown
    STDMETHODIMP QueryInterface(REFIID riid, void** ppv) {
        if (!ppv) return E_POINTER;
        if (riid == IID_IUnknown || riid == IID_IBaseFilter || riid == IID_IMediaFilter) {
            *ppv = static_cast<IBaseFilter*>(this);
        } else if (riid == IID_IPin) {
            *ppv = static_cast<IPin*>(this);
        } else if (riid == IID_IMemInputPin) {
            *ppv = static_cast<IMemInputPin*>(this);
        } else {
            *ppv = NULL;
            return E_NOINTERFACE;
        }
        AddRef();
        return S_OK;
    }
    STDMETHODIMP_(ULONG) AddRef() { return InterlockedIncrement(&m_refCount); }
    STDMETHODIMP_(ULONG) Release() {
        ULONG count = InterlockedDecrement(&m_refCount);
        if (count == 0) delete this;
        return count;
    }

    // IPersist
    STDMETHODIMP GetClassID(CLSID* pClsid) { if (!pClsid) return E_POINTER; *pClsid = GUID_NULL; return S_OK; }

    // IMediaFilter
    STDMETHODIMP Stop() { m_state = State_Stopped; return S_OK; }
    STDMETHODIMP Pause() { m_state = State_Paused; return S_OK; }
    STDMETHODIMP Run(REFERENCE_TIME tStart) { m_state = State_Running; return S_OK; }
    STDMETHODIMP GetState(DWORD dwMilliSecsTimeout, FILTER_STATE* State) {
        if (!State) return E_POINTER;
        *State = m_state;
        return S_OK;
    }
    STDMETHODIMP SetSyncSource(IReferenceClock* pClock) { return S_OK; }
    STDMETHODIMP GetSyncSource(IReferenceClock** ppClock) { if (!ppClock) return E_POINTER; *ppClock = NULL; return S_OK; }

    // IBaseFilter
    STDMETHODIMP EnumPins(IEnumPins** ppEnum) {
        if (!ppEnum) return E_POINTER;
        *ppEnum = new CEnumPins(this);
        return S_OK;
    }
    STDMETHODIMP FindPin(LPCWSTR Id, IPin** ppPin) {
        if (!ppPin) return E_POINTER;
        *ppPin = static_cast<IPin*>(this);
        AddRef();
        return S_OK;
    }
    STDMETHODIMP QueryFilterInfo(FILTER_INFO* pInfo) {
        if (!pInfo) return E_POINTER;
        pInfo->achName[0] = 0;
        pInfo->pGraph = m_pGraph;
        if (m_pGraph) m_pGraph->AddRef();
        return S_OK;
    }
    STDMETHODIMP JoinFilterGraph(IFilterGraph* pGraph, LPCWSTR pName) {
        m_pGraph = pGraph;
        return S_OK;
    }
    STDMETHODIMP QueryVendorInfo(LPWSTR* pVendorInfo) { return E_NOTIMPL; }

    // IPin
    STDMETHODIMP Connect(IPin* pReceivePin, const AM_MEDIA_TYPE* pmt) { return E_UNEXPECTED; }
    STDMETHODIMP ReceiveConnection(IPin* pConnector, const AM_MEDIA_TYPE* pmt) {
        m_pConnectedPin = pConnector;
        if (m_pConnectedPin) m_pConnectedPin->AddRef();
        if (pmt) m_mediaType = *pmt;
        return S_OK;
    }
    STDMETHODIMP Disconnect() {
        if (m_pConnectedPin) { m_pConnectedPin->Release(); m_pConnectedPin = NULL; }
        return S_OK;
    }
    STDMETHODIMP ConnectedTo(IPin** pPin) {
        if (!pPin) return E_POINTER;
        *pPin = m_pConnectedPin;
        if (*pPin) (*pPin)->AddRef();
        return m_pConnectedPin ? S_OK : VFW_E_NOT_CONNECTED;
    }
    STDMETHODIMP ConnectionMediaType(AM_MEDIA_TYPE* pmt) {
        if (!pmt) return E_POINTER;
        *pmt = m_mediaType;
        return S_OK;
    }
    STDMETHODIMP QueryPinInfo(PIN_INFO* pInfo) {
        if (!pInfo) return E_POINTER;
        pInfo->pFilter = static_cast<IBaseFilter*>(this);
        pInfo->pFilter->AddRef();
        pInfo->dir = PINDIR_INPUT;
        lstrcpyW(pInfo->achName, L"TS_In");
        return S_OK;
    }
    STDMETHODIMP QueryDirection(PIN_DIRECTION* pPinDir) {
        if (!pPinDir) return E_POINTER;
        *pPinDir = PINDIR_INPUT;
        return S_OK;
    }
    STDMETHODIMP QueryId(LPWSTR* Id) { return E_NOTIMPL; }
    STDMETHODIMP QueryAccept(const AM_MEDIA_TYPE* pmt) { return S_OK; }
    STDMETHODIMP EnumMediaTypes(IEnumMediaTypes** ppEnum) { return E_NOTIMPL; }
    STDMETHODIMP QueryInternalConnections(IPin** apPin, ULONG* nPin) { return E_NOTIMPL; }
    STDMETHODIMP EndOfStream() { return S_OK; }
    STDMETHODIMP BeginFlush() { return S_OK; }
    STDMETHODIMP EndFlush() { return S_OK; }
    STDMETHODIMP NewSegment(REFERENCE_TIME tStart, REFERENCE_TIME tStop, double dRate) { return S_OK; }

    // IMemInputPin
    STDMETHODIMP GetAllocator(IMemAllocator** ppAllocator) { return VFW_E_NO_ALLOCATOR; }
    STDMETHODIMP NotifyAllocator(IMemAllocator* pAllocator, BOOL bReadOnly) { return S_OK; }
    STDMETHODIMP GetAllocatorRequirements(ALLOCATOR_PROPERTIES* pProps) { return E_NOTIMPL; }
    STDMETHODIMP Receive(IMediaSample* pSample) {
        if (!pSample) return E_POINTER;
        BYTE* pBuffer = NULL;
        if (SUCCEEDED(pSample->GetPointer(&pBuffer)) && pBuffer) {
            long len = pSample->GetActualDataLength();
            if (len > 0) {
                DWORD written = 0;
                WriteFile(m_hStdOut, pBuffer, (DWORD)len, &written, NULL);
            }
        }
        return S_OK;
    }
    STDMETHODIMP ReceiveMultiple(IMediaSample** pSamples, long nSamples, long* nSamplesProcessed) {
        for (long i = 0; i < nSamples; i++) Receive(pSamples[i]);
        if (nSamplesProcessed) *nSamplesProcessed = nSamples;
        return S_OK;
    }
    STDMETHODIMP ReceiveCanBlock() { return S_OK; }
};

CEnumPins::CEnumPins(CTSSink* pSink) : m_refCount(1), m_pSink(pSink), m_pos(0) {
    if (m_pSink) m_pSink->AddRef();
}
CEnumPins::~CEnumPins() {
    if (m_pSink) m_pSink->Release();
}
STDMETHODIMP CEnumPins::QueryInterface(REFIID riid, void** ppv) {
    if (!ppv) return E_POINTER;
    if (riid == IID_IUnknown || riid == IID_IEnumPins) {
        *ppv = static_cast<IEnumPins*>(this);
        AddRef();
        return S_OK;
    }
    *ppv = NULL;
    return E_NOINTERFACE;
}
STDMETHODIMP_(ULONG) CEnumPins::AddRef() { return InterlockedIncrement(&m_refCount); }
STDMETHODIMP_(ULONG) CEnumPins::Release() {
    ULONG count = InterlockedDecrement(&m_refCount);
    if (count == 0) delete this;
    return count;
}
STDMETHODIMP CEnumPins::Next(ULONG cPins, IPin** ppPins, ULONG* pcFetched) {
    if (!ppPins) return E_POINTER;
    if (m_pos == 0 && cPins > 0) {
        ppPins[0] = static_cast<IPin*>(m_pSink);
        ppPins[0]->AddRef();
        m_pos = 1;
        if (pcFetched) *pcFetched = 1;
        return cPins == 1 ? S_OK : S_FALSE;
    }
    if (pcFetched) *pcFetched = 0;
    return S_FALSE;
}
STDMETHODIMP CEnumPins::Skip(ULONG cPins) { m_pos += cPins; return S_OK; }
STDMETHODIMP CEnumPins::Reset() { m_pos = 0; return S_OK; }
STDMETHODIMP CEnumPins::Clone(IEnumPins** ppEnum) { return E_NOTIMPL; }

// Helper function to connect output pin of pSrc to input pin of pDest
HRESULT ConnectFilters(IGraphBuilder* pGraph, IBaseFilter* pSrc, IBaseFilter* pDest) {
    IEnumPins* pEnumSrc = NULL;
    if (FAILED(pSrc->EnumPins(&pEnumSrc))) return E_FAIL;
    IPin* pOutPin = NULL;
    while (pEnumSrc->Next(1, &pOutPin, NULL) == S_OK) {
        PIN_DIRECTION dir;
        pOutPin->QueryDirection(&dir);
        if (dir == PINDIR_OUTPUT) {
            IPin* pConnected = NULL;
            pOutPin->ConnectedTo(&pConnected);
            if (!pConnected) {
                IEnumPins* pEnumDest = NULL;
                if (SUCCEEDED(pDest->EnumPins(&pEnumDest))) {
                    IPin* pInPin = NULL;
                    while (pEnumDest->Next(1, &pInPin, NULL) == S_OK) {
                        PIN_DIRECTION inDir;
                        pInPin->QueryDirection(&inDir);
                        if (inDir == PINDIR_INPUT) {
                            IPin* pInConnected = NULL;
                            pInPin->ConnectedTo(&pInConnected);
                            if (!pInConnected) {
                                HRESULT hr = pGraph->ConnectDirect(pOutPin, pInPin, NULL);
                                if (FAILED(hr)) hr = pGraph->Connect(pOutPin, pInPin);
                                if (SUCCEEDED(hr)) {
                                    pInPin->Release();
                                    pEnumDest->Release();
                                    pOutPin->Release();
                                    pEnumSrc->Release();
                                    return S_OK;
                                }
                            }
                            if (pInConnected) pInConnected->Release();
                        }
                        pInPin->Release();
                    }
                    pEnumDest->Release();
                }
            }
            if (pConnected) pConnected->Release();
        }
        pOutPin->Release();
    }
    pEnumSrc->Release();
    return E_FAIL;
}

// Enumerate BDA Network Tuners and print JSON
void ListTuners() {
    ICreateDevEnum* pDevEnum = NULL;
    if (FAILED(CoCreateInstance(CLSID_SystemDeviceEnum, NULL, CLSCTX_INPROC_SERVER,
                                IID_ICreateDevEnum, (void**)&pDevEnum))) {
        printf("[]\n");
        return;
    }

    IEnumMoniker* pEnum = NULL;
    if (FAILED(pDevEnum->CreateClassEnumerator(KSCATEGORY_BDA_NETWORK_TUNER, &pEnum, 0)) || !pEnum) {
        pDevEnum->Release();
        printf("[]\n");
        return;
    }

    printf("[");
    IMoniker* pMoniker = NULL;
    int idx = 0;
    bool first = true;
    while (pEnum->Next(1, &pMoniker, NULL) == S_OK) {
        IPropertyBag* pPropBag = NULL;
        if (SUCCEEDED(pMoniker->BindToStorage(0, 0, IID_IPropertyBag, (void**)&pPropBag))) {
            VARIANT var;
            VariantInit(&var);
            if (SUCCEEDED(pPropBag->Read(L"FriendlyName", &var, 0))) {
                if (!first) printf(",");
                char nameBuf[256] = {0};
                WideCharToMultiByte(CP_UTF8, 0, var.bstrVal, -1, nameBuf, sizeof(nameBuf) - 1, NULL, NULL);
                printf("{\"index\":%d,\"name\":\"%s\"}", idx, nameBuf);
                first = false;
                idx++;
            }
            VariantClear(&var);
            pPropBag->Release();
        }
        pMoniker->Release();
    }
    printf("]\n");

    pEnum->Release();
    pDevEnum->Release();
}

int main(int argc, char* argv[]) {
    HRESULT hr = CoInitializeEx(NULL, COINIT_MULTITHREADED);
    if (FAILED(hr)) {
        fprintf(stderr, "[bda-runner] Failed to initialize COM: 0x%08lx\n", hr);
        return 1;
    }

    bool listMode = false;
    int tunerIndex = 0;
    long long freqHz = 0;
    int bwHz = 8000000;

    for (int i = 1; i < argc; i++) {
        if (strcmp(argv[i], "--list") == 0) {
            listMode = true;
        } else if (strcmp(argv[i], "--tuner") == 0 && i + 1 < argc) {
            tunerIndex = atoi(argv[++i]);
        } else if (strcmp(argv[i], "--freq") == 0 && i + 1 < argc) {
            freqHz = _atoi64(argv[++i]);
        } else if (strcmp(argv[i], "--bw") == 0 && i + 1 < argc) {
            bwHz = atoi(argv[++i]);
        }
    }

    if (listMode) {
        ListTuners();
        CoUninitialize();
        return 0;
    }

    if (freqHz <= 0) {
        fprintf(stderr, "Usage: bda-runner.exe --list OR --freq <Hz> [--bw <Hz>] [--tuner <index>]\n");
        CoUninitialize();
        return 1;
    }

    // Set binary mode for standard output
    _setmode(_fileno(stdout), _O_BINARY);

    IGraphBuilder* pGraph = NULL;
    hr = CoCreateInstance(CLSID_FilterGraph, NULL, CLSCTX_INPROC_SERVER,
                          IID_IGraphBuilder, (void**)&pGraph);
    if (FAILED(hr)) {
        fprintf(stderr, "[bda-runner] Failed to create FilterGraph: 0x%08lx\n", hr);
        CoUninitialize();
        return 1;
    }

    // 1. DVB-T Network Provider
    IBaseFilter* pNetworkProvider = NULL;
    hr = CoCreateInstance(CLSID_DVBTNetworkProvider, NULL, CLSCTX_INPROC_SERVER,
                          IID_IBaseFilter, (void**)&pNetworkProvider);
    if (FAILED(hr)) {
        fprintf(stderr, "[bda-runner] Failed to create DVB-T Network Provider: 0x%08lx\n", hr);
        pGraph->Release();
        CoUninitialize();
        return 1;
    }
    pGraph->AddFilter(pNetworkProvider, L"DVB-T Network Provider");

    // 2. Locate and add the specified BDA Tuner filter
    ICreateDevEnum* pDevEnum = NULL;
    CoCreateInstance(CLSID_SystemDeviceEnum, NULL, CLSCTX_INPROC_SERVER,
                     IID_ICreateDevEnum, (void**)&pDevEnum);
    IEnumMoniker* pEnumTuner = NULL;
    IBaseFilter* pTunerFilter = NULL;
    if (pDevEnum && SUCCEEDED(pDevEnum->CreateClassEnumerator(KSCATEGORY_BDA_NETWORK_TUNER, &pEnumTuner, 0)) && pEnumTuner) {
        IMoniker* pMoniker = NULL;
        int curIdx = 0;
        while (pEnumTuner->Next(1, &pMoniker, NULL) == S_OK) {
            if (curIdx == tunerIndex) {
                pMoniker->BindToObject(0, 0, IID_IBaseFilter, (void**)&pTunerFilter);
                pMoniker->Release();
                break;
            }
            curIdx++;
            pMoniker->Release();
        }
        pEnumTuner->Release();
    }
    if (!pTunerFilter) {
        fprintf(stderr, "[bda-runner] BDA Tuner index %d not found\n", tunerIndex);
        pNetworkProvider->Release();
        pGraph->Release();
        if (pDevEnum) pDevEnum->Release();
        CoUninitialize();
        return 1;
    }
    pGraph->AddFilter(pTunerFilter, L"BDA Tuner");

    // Connect Network Provider to Tuner
    ConnectFilters(pGraph, pNetworkProvider, pTunerFilter);

    // 3. Locate and add BDA Receiver/Capture filter
    IBaseFilter* pCaptureFilter = NULL;
    IEnumMoniker* pEnumCapture = NULL;
    if (pDevEnum && SUCCEEDED(pDevEnum->CreateClassEnumerator(KSCATEGORY_BDA_RECEIVER_COMPONENT, &pEnumCapture, 0)) && pEnumCapture) {
        IMoniker* pMoniker = NULL;
        if (pEnumCapture->Next(1, &pMoniker, NULL) == S_OK) {
            pMoniker->BindToObject(0, 0, IID_IBaseFilter, (void**)&pCaptureFilter);
            pMoniker->Release();
        }
        pEnumCapture->Release();
    }
    if (pDevEnum) pDevEnum->Release();

    if (pCaptureFilter) {
        pGraph->AddFilter(pCaptureFilter, L"BDA Capture");
        ConnectFilters(pGraph, pTunerFilter, pCaptureFilter);
    }

    // 4. Custom TS Sink Filter
    CTSSink* pSink = new CTSSink();
    pGraph->AddFilter(pSink, L"TS Sink");

    IBaseFilter* pLastFilter = pCaptureFilter ? pCaptureFilter : pTunerFilter;
    ConnectFilters(pGraph, pLastFilter, pSink);

    // 5. Submit Tune Request
    ITuner* pTuner = NULL;
    if (SUCCEEDED(pNetworkProvider->QueryInterface(__uuidof(ITuner), (void**)&pTuner))) {
        IDVBTuningSpace2* pTuningSpace = NULL;
        if (SUCCEEDED(CoCreateInstance(CLSID_DVBTuningSpace, NULL, CLSCTX_INPROC_SERVER,
                                      __uuidof(IDVBTuningSpace2), (void**)&pTuningSpace))) {
            ITuneRequest* pTuneReq = NULL;
            if (SUCCEEDED(pTuningSpace->CreateTuneRequest(&pTuneReq))) {
                IDVBTLocator* pLocator = NULL;
                if (SUCCEEDED(CoCreateInstance(CLSID_DVBTLocator, NULL, CLSCTX_INPROC_SERVER,
                                              __uuidof(IDVBTLocator), (void**)&pLocator))) {
                    long freqKHz = (long)(freqHz / 1000);
                    long bwMHz = (long)(bwHz / 1000000);
                    if (bwMHz == 0) bwMHz = 8;
                    pLocator->put_CarrierFrequency(freqKHz);
                    pLocator->put_Bandwidth(bwMHz);

                    pTuneReq->put_Locator(pLocator);
                    pTuner->put_TuneRequest(pTuneReq);

                    fprintf(stderr, "[bda-runner] Tuned to %ld kHz, BW: %ld MHz\n", freqKHz, bwMHz);

                    pLocator->Release();
                }
                pTuneReq->Release();
            }
            pTuningSpace->Release();
        }
        pTuner->Release();
    }

    // 6. Run graph
    IMediaControl* pControl = NULL;
    pGraph->QueryInterface(IID_IMediaControl, (void**)&pControl);
    if (pControl) {
        pControl->Run();
        fprintf(stderr, "[bda-runner] DirectShow BDA graph running. Streaming TS to stdout...\n");
    }

    // Wait until parent closes standard input or sends EOF
    HANDLE hStdIn = GetStdHandle(STD_INPUT_HANDLE);
    char dummy[128];
    DWORD bytesRead = 0;
    while (ReadFile(hStdIn, dummy, sizeof(dummy), &bytesRead, NULL)) {
        if (bytesRead == 0) break;
    }

    fprintf(stderr, "[bda-runner] Stopping DirectShow graph...\n");
    if (pControl) {
        pControl->Stop();
        pControl->Release();
    }

    pSink->Release();
    if (pCaptureFilter) pCaptureFilter->Release();
    pTunerFilter->Release();
    pNetworkProvider->Release();
    pGraph->Release();

    CoUninitialize();
    return 0;
}
