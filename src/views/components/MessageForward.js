import FormRecipient from "./generic/FormRecipient.js";

export default {
    name: 'ForwardMessage',
    components: {
        FormRecipient
    },
    data() {
        return {
            type: window.TYPEUSER,
            phone: '',
            message_id: '',
            is_forwarded: true,
            loading: false,
        }
    },
    computed: {
        chat_id() {
            if (this.type === window.TYPESTATUS) {
                return window.TYPESTATUS;
            }
            return this.phone + this.type;
        }
    },
    methods: {
        openModal() {
            $('#modalMessageForward').modal({
                onApprove: function () {
                    return false;
                }
            }).modal('show');
        },
        isValidForm() {
            if (this.type !== window.TYPESTATUS && !this.phone.trim()) {
                return false;
            }

            if (!this.message_id.trim()) {
                return false;
            }

            return true;
        },
        async handleSubmit() {
            if (!this.isValidForm() || this.loading) {
                return;
            }

            try {
                let response = await this.submitApi()
                showSuccessInfo(response)
                $('#modalMessageForward').modal('hide');
            } catch (err) {
                showErrorInfo(err)
            }
        },
        async submitApi() {
            this.loading = true;
            try {
                const payload = {
                    chat_id: this.chat_id,
                    is_forwarded: this.is_forwarded,
                }
                let response = await window.http.post(`/message/${this.message_id}/forward`, payload)
                this.handleReset();
                return response.data.message;
            } catch (error) {
                if (error.response) {
                    throw new Error(error.response.data.message);
                }
                throw new Error(error.message);
            } finally {
                this.loading = false;
            }
        },
        handleReset() {
            this.type = window.TYPEUSER;
            this.phone = '';
            this.message_id = '';
            this.is_forwarded = true;
            this.loading = false;
        },
    },
    template: `
    <div class="red card" @click="openModal()" style="cursor: pointer">
        <div class="content">
            <a class="ui red right ribbon label">Message</a>
            <div class="header">Forward Message</div>
            <div class="description">
                Forward a stored message to another chat
            </div>
        </div>
    </div>
        
    <div class="ui small modal" id="modalMessageForward">
        <i class="close icon"></i>
        <div class="header">
            Forward Message
        </div>
        <div class="content">
            <form class="ui form">
                <FormRecipient v-model:type="type" v-model:phone="phone"/>
                
                <div class="field">
                    <label>Message ID</label>
                    <input v-model="message_id" type="text" placeholder="Please enter the message id to forward"
                           aria-label="message id">
                </div>

                <div class="field">
                    <div class="ui checkbox">
                        <input type="checkbox" aria-label="is forwarded" v-model="is_forwarded">
                        <label>Mark message as forwarded</label>
                    </div>
                </div>
            </form>
        </div>
        <div class="actions">
            <button class="ui approve positive right labeled icon button" :class="{'loading': this.loading, 'disabled': !isValidForm() || loading}"
                 @click.prevent="handleSubmit">
                Forward
                <i class="share icon"></i>
            </button>
        </div>
    </div>
    `
}
